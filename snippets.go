package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	// lockTimeout bounds how long a mutation waits for a competing process.
	lockTimeout = 2 * time.Second
	// lockStale is when a lock is assumed abandoned by a killed process.
	lockStale = 30 * time.Second

	snippetExt   = ".md"
	manifestName = ".enabled"
	filePerm     = 0o644
	dirPerm      = 0o755
)

// validName rejects anything that could escape the store directory or collide
// with the manifest. A snippet name becomes a file name directly, so this is
// the only thing between a mistyped argument and `snip rm ../../notes`.
func validName(name string) error {
	switch {
	case name == "":
		return errors.New("snippet name is empty")
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("snippet name %q cannot contain a path separator", name)
	case strings.HasPrefix(name, "."):
		return fmt.Errorf("snippet name %q cannot start with a dot", name)
	case name != filepath.Clean(name):
		return fmt.Errorf("snippet name %q is not a plain file name", name)
	}
	return nil
}

// Store is the on-disk snippet collection: one .md file per snippet plus a
// .enabled manifest listing, one per line, the snippets currently appended to
// every prompt.
type Store struct {
	dir string
}

// newStoreAt builds a store rooted at an existing directory.
func newStoreAt(dir string) *Store { return &Store{dir: dir} }

// Dir is the directory holding this scope's snippet files and manifest.
func (s *Store) Dir() string { return s.dir }

// Path is the file backing one snippet.
func (s *Store) Path(name string) string { return filepath.Join(s.dir, name+snippetExt) }

func NewStore() (*Store, error) {
	dir := os.Getenv("SNIP_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".claude", "snippets")
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, err
	}
	return newStoreAt(dir), nil
}

func (s *Store) manifestPath() string { return filepath.Join(s.dir, manifestName) }

// Names lists every snippet on disk, sorted.
func (s *Store) Names() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), snippetExt) {
			continue
		}
		out = append(out, strings.TrimSuffix(entry.Name(), snippetExt))
	}
	slices.Sort(out)
	return out, nil
}

// Exists reports whether the store holds this snippet. An invalid name is
// simply absent, so every lookup path inherits the traversal guard.
func (s *Store) Exists(name string) bool {
	if validName(name) != nil {
		return false
	}
	info, err := os.Stat(s.Path(name))
	return err == nil && !info.IsDir()
}

func (s *Store) Body(name string) (string, error) {
	if err := validName(name); err != nil {
		return "", err
	}
	b, err := os.ReadFile(s.Path(name))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

// Summary is the snippet's first line, for pickers and status output.
func (s *Store) Summary(name string) string {
	body, err := s.Body(name)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(body, "\n")
	return strings.TrimSpace(line)
}

func (s *Store) Write(name, body string) error {
	if err := validName(name); err != nil {
		return err
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return os.WriteFile(s.Path(name), []byte(body), filePerm)
}

// RemoveAll deletes snippets from disk and drops them from the manifest. Every
// name is validated before anything is removed.
func (s *Store) RemoveAll(names ...string) error {
	for _, name := range names {
		if err := validName(name); err != nil {
			return err
		}
	}
	for _, name := range names {
		if err := os.Remove(s.Path(name)); err != nil {
			return err
		}
	}
	return s.Disable(names...)
}

// Enabled returns the manifest, filtered to snippets that still exist so a
// deleted file never breaks the hook.
func (s *Store) Enabled() ([]string, error) {
	b, err := os.ReadFile(s.manifestPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		name := strings.TrimSpace(line)
		if name == "" || strings.HasPrefix(name, "#") || seen[name] || !s.Exists(name) {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

func (s *Store) IsEnabled(name string) bool {
	on, _ := s.Enabled()
	for _, n := range on {
		if n == name {
			return true
		}
	}
	return false
}

// SetEnabled replaces the manifest wholesale, preserving the given order.
func (s *Store) SetEnabled(names []string) error {
	return s.mutate(func([]string) []string { return names })
}

// mutate applies fn to the manifest under an exclusive lock and writes the
// result atomically. Every read-modify-write goes through here, so two Claude
// sessions toggling at once cannot lose an update.
func (s *Store) mutate(fn func(current []string) []string) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()

	current, err := s.Enabled()
	if err != nil {
		return err
	}
	var b strings.Builder
	seen := map[string]bool{}
	for _, n := range fn(current) {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		b.WriteString(n)
		b.WriteString("\n")
	}
	return writeFileAtomic(s.manifestPath(), []byte(b.String()))
}

// lock takes an advisory lock by exclusive file creation, which is atomic on
// every filesystem we care about and needs no syscall package. A stale lock
// older than lockStale is reclaimed so a killed process cannot wedge the tool.
func (s *Store) lock() (func(), error) {
	path := s.manifestPath() + ".lock"
	deadline := time.Now().Add(lockTimeout)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, filePerm)
		if err == nil {
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if st, serr := os.Stat(path); serr == nil && time.Since(st.ModTime()) > lockStale {
			os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for %s (remove it if no other snip is running)", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// writeFileAtomic avoids a torn manifest if the process dies mid-write.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), filePerm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (s *Store) Enable(names ...string) error {
	return s.mutate(func(on []string) []string { return append(on, names...) })
}

func (s *Store) Disable(names ...string) error {
	drop := map[string]bool{}
	for _, n := range names {
		drop[n] = true
	}
	return s.mutate(func(on []string) []string {
		var keep []string
		for _, n := range on {
			if !drop[n] {
				keep = append(keep, n)
			}
		}
		return keep
	})
}

// Toggle flips one snippet and reports its new state. The read and the write
// happen under one lock so a concurrent toggle cannot invert the result.
func (s *Store) Toggle(name string) (bool, error) {
	var nowOn bool
	err := s.mutate(func(on []string) []string {
		for i, n := range on {
			if n == name {
				return append(append([]string{}, on[:i]...), on[i+1:]...)
			}
		}
		nowOn = true
		return append(on, name)
	})
	return nowOn, err
}

// Compose joins every enabled snippet into the text appended to each prompt.
func (s *Store) Compose() (string, error) {
	on, err := s.Enabled()
	if err != nil {
		return "", err
	}
	var parts []string
	for _, n := range on {
		body, err := s.Body(n)
		if err != nil {
			continue
		}
		if strings.TrimSpace(body) != "" {
			parts = append(parts, body)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}
