package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
)

// lockTimeout bounds how long a mutation waits for a competing process, and
// lockStale is when a lock is assumed abandoned by a killed process. They are
// vars so a test can exercise the timeout without spending it.
var (
	lockTimeout = 2 * time.Second
	lockStale   = 30 * time.Second
)

const (
	snippetExt   = ".md"
	manifestName = ".enabled"
	seenName     = ".seen"
	filePerm     = 0o644
	dirPerm      = 0o755

	// maxSnippetBytes bounds a snippet body. Every enabled snippet is read and
	// concatenated on every prompt, inside the hook's 5s timeout, so an
	// accidental "snip add notes < server.log" would not fail loudly: it would
	// quietly make each prompt slower and larger until the hook timed out.
	maxSnippetBytes = 64 << 10
	// summaryMax bounds the prefix Summary reads. A summary is one short line,
	// but listings call Summary once per row, so reading each body in full made
	// "snip" and "snip menu" cost the size of the whole corpus rather than the
	// size of what they display.
	summaryMax = 4 << 10
)

// validName rejects anything that could escape the store directory or that the
// manifest cannot carry. A snippet name becomes a file name directly, so this
// is the only thing between a mistyped argument and `snip rm ../../notes`.
//
// The second half matters just as much, if less loudly: .enabled holds one name
// per line, trims each line before matching it, and treats "#" as a comment. A
// name that does not survive that round trip could be created and enabled with
// every command reporting success, yet never reach a prompt.
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
	// filepath.IsLocal is platform-aware where the checks above are not: on
	// Windows it also rejects drive-relative names like "C:x" and the reserved
	// device names ("con", "nul", "com1", extension ignored), which name a
	// device rather than a file in the store. Releases ship Windows binaries.
	case !filepath.IsLocal(name):
		return fmt.Errorf("snippet name %q is reserved or non-local on this platform", name)
	case strings.HasPrefix(name, "#"):
		return fmt.Errorf("snippet name %q cannot start with a hash; the manifest reads that as a comment", name)
	case name != strings.TrimSpace(name):
		return fmt.Errorf("snippet name %q cannot start or end with whitespace", name)
	case strings.ContainsFunc(name, unicode.IsControl):
		return fmt.Errorf("snippet name %q cannot contain a control character", name)
	}
	return nil
}

// Store is the on-disk snippet collection: one .md file per snippet plus a
// .enabled manifest listing, one per line, the snippets currently appended to
// every prompt.
type Store struct {
	dir string
	// manifest is the enabled list, and it is the user's own file. For the
	// project scope it deliberately does not live in dir: see suggest.
	manifest string
	// seen records suggestions the user has already decided on, so a proposal
	// they declined does not reappear on every listing. Empty when the scope
	// has no suggestions to track.
	seen string
	// suggest is a repository-supplied enabled list, read as a proposal and
	// never applied. A clone must not be able to change what a prompt carries,
	// so the repository proposes and the user disposes. Empty for the global
	// scope, which is the user's own directory and needs no such distinction.
	suggest string
	// untrusted marks a store whose directory arrives with a git clone rather
	// than being the user's own. Only the project scope is untrusted: it lives
	// inside whatever repository the working directory sits in, and its
	// contents are appended verbatim to every prompt. See statSnippet.
	untrusted bool
}

// newStoreAt builds a store rooted at an existing directory, holding both its
// snippets and its enabled list.
func newStoreAt(dir string) *Store {
	return &Store{dir: dir, manifest: filepath.Join(dir, manifestName)}
}

// newProjectStoreAt builds the project-scope store: snippet files come from the
// repository at dir, while the enabled list and the record of decided
// suggestions live under state, inside the user's own store. state is a path
// prefix, not a directory, so the two files sit beside each other.
func newProjectStoreAt(dir, state string) *Store {
	return &Store{
		dir:       dir,
		manifest:  state + manifestName,
		seen:      state + seenName,
		suggest:   filepath.Join(dir, manifestName),
		untrusted: true,
	}
}

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

func (s *Store) manifestPath() string { return s.manifest }

// ensureStateDir creates the directory holding this scope's state files. For
// the project scope that is inside the user's own store, which also means a
// read-only checkout can still be toggled.
func (s *Store) ensureStateDir() error {
	return os.MkdirAll(filepath.Dir(s.manifest), dirPerm)
}

// statSnippet resolves one snippet file and applies this store's file policy,
// so every read path — list, show, compose, enable — enforces the same rules.
//
// A snippet must be a regular file: a directory or a fifo named "brief.md"
// would otherwise be offered as a snippet that no read can ever complete.
//
// In an untrusted store it must not be a symbolic link. Snippet bodies are
// pasted verbatim into every prompt, so a link is a read primitive, and it is
// one git can commit: .claude/snippets/notes.md -> ../../../../.ssh/id_ed25519
// arrives with a clone and needs nothing from the user but a cd. The global
// store is exempt because it is the user's own directory, where linking a
// snippet into a dotfiles repository is an ordinary thing to do.
func (s *Store) statSnippet(name string) error {
	if err := validName(name); err != nil {
		return err
	}
	path := s.Path(name)
	if s.untrusted {
		li, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if li.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("snippet %q is a symbolic link; a project snippet must be a regular file in %s", name, s.dir)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("snippet %q is not a regular file", name)
	}
	return nil
}

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
		name := strings.TrimSuffix(entry.Name(), snippetExt)
		// A file no snippet name could have produced is not a snippet. Every
		// other lookup path already rejects such a name, so listing it would
		// offer a row that nothing — toggle, show, rm — can act on.
		if validName(name) != nil {
			continue
		}
		// Listing is a promise that the row can be turned on and shown, so a
		// file the read policy rejects is not listed either. Only untrusted
		// stores pay the extra stat; the global scope keeps the cheap path.
		if s.untrusted && s.statSnippet(name) != nil {
			continue
		}
		out = append(out, name)
	}
	slices.Sort(out)
	return out, nil
}

// Exists reports whether the store holds this snippet. An invalid name is
// simply absent, so every lookup path inherits the traversal guard.
func (s *Store) Exists(name string) bool { return s.statSnippet(name) == nil }

func (s *Store) Body(name string) (string, error) {
	if err := s.statSnippet(name); err != nil {
		return "", err
	}
	b, err := os.ReadFile(s.Path(name))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

// Summary is the snippet's first line, for pickers and status output. It reads
// a bounded prefix rather than the body: listings call this once per row, and
// reading each file in full made rendering scale with the size of the corpus
// instead of with the number of rows shown.
func (s *Store) Summary(name string) string {
	if err := s.statSnippet(name); err != nil {
		return ""
	}
	f, err := os.Open(s.Path(name))
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, summaryMax)
	n, err := io.ReadFull(f, buf)
	if n == 0 && err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(buf[:n]), "\n")
	return strings.TrimSpace(line)
}

func (s *Store) Write(name, body string) error {
	if err := validName(name); err != nil {
		return err
	}
	if len(body) > maxSnippetBytes {
		return fmt.Errorf("snippet %q is %d bytes; a snippet is appended to every prompt, so the limit is %d",
			name, len(body), maxSnippetBytes)
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

// readManifest parses one name-per-line list. Blank lines and "#" comments are
// skipped, each line is trimmed before it counts, and repeats collapse. A file
// that is not there is an empty list rather than an error, so a scope the user
// has never touched behaves like one they emptied.
func readManifest(path string) ([]string, error) {
	b, err := os.ReadFile(path)
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
		if name == "" || strings.HasPrefix(name, "#") || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

// present filters a name list to the snippets this store actually holds, so a
// deleted file never breaks the hook and a name the file policy rejects never
// reaches a prompt.
func (s *Store) present(names []string) []string {
	var out []string
	for _, name := range names {
		if s.Exists(name) {
			out = append(out, name)
		}
	}
	return out
}

// Enabled returns the user's enabled list, filtered to snippets that still
// exist. For the project scope this reads the user's own file, never the
// repository's — see Suggested.
func (s *Store) Enabled() ([]string, error) {
	names, err := readManifest(s.manifest)
	if err != nil {
		return nil, err
	}
	return s.present(names), nil
}

// Suggested is what the repository proposes and the user has not yet decided
// on: the entries of its .enabled that are neither already enabled nor
// previously acknowledged. It is only ever reported, never applied. Cloning a
// repository must not change what a prompt carries, and "the user already
// opted in by cd-ing there" is not consent when the directory that opts in is
// itself part of the clone.
func (s *Store) Suggested() ([]string, error) {
	if s.suggest == "" {
		return nil, nil
	}
	proposed, err := readManifest(s.suggest)
	if err != nil {
		return nil, err
	}
	if len(proposed) == 0 {
		return nil, nil
	}
	decided := map[string]bool{}
	acked, err := readManifest(s.seen)
	if err != nil {
		return nil, err
	}
	for _, name := range acked {
		decided[name] = true
	}
	on, err := s.Enabled()
	if err != nil {
		return nil, err
	}
	for _, name := range on {
		decided[name] = true
	}
	var out []string
	for _, name := range s.present(proposed) {
		if !decided[name] {
			out = append(out, name)
		}
	}
	return out, nil
}

// Acknowledge records that the user has decided on these suggestions. Without
// it a proposal they declined would be re-offered on every listing, and the
// only way to silence it would be to accept it.
func (s *Store) Acknowledge(names ...string) error {
	if s.seen == "" || len(names) == 0 {
		return nil
	}
	if err := s.ensureStateDir(); err != nil {
		return err
	}
	current, err := readManifest(s.seen)
	if err != nil {
		return err
	}
	var b strings.Builder
	seen := map[string]bool{}
	for _, name := range append(current, names...) {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		b.WriteString(name)
		b.WriteString("\n")
	}
	return writeFileAtomic(s.seen, []byte(b.String()))
}

// Adopt turns suggestions on and marks them decided; Dismiss only marks them,
// leaving them off. Both are the explicit user action that a repository's own
// .enabled deliberately is not.
func (s *Store) Adopt(names []string) error {
	if err := s.Enable(names...); err != nil {
		return err
	}
	return s.Acknowledge(names...)
}

func (s *Store) Dismiss(names []string) error { return s.Acknowledge(names...) }

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
	if err := s.ensureStateDir(); err != nil {
		return err
	}
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
		// Reclaim a lock a killed process left behind. A removal that fails —
		// a read-only directory, another user's file under a sticky bit — must
		// fall through to the deadline below rather than loop: retrying it
		// unconditionally skipped both the timeout and the sleep, spinning on
		// the CPU until the process was killed.
		if st, serr := os.Stat(path); serr == nil && time.Since(st.ModTime()) > lockStale {
			if rerr := os.Remove(path); rerr == nil {
				continue
			}
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
	// Rename orders the directory entry, not the data behind it. Without this
	// a crash between write and flush can publish a manifest of trailing NULs,
	// which is worse than the torn write the rename was there to avoid.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), filePerm); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	// Best effort: not every platform lets a directory be synced, and the
	// manifest is already durable enough at this point to be worth no error.
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
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
