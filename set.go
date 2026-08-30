package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Scope names where a snippet lives. Project snippets shadow global ones of
// the same name for bare-name lookups, but both remain listable and enable
// independently — the two files are separate snippets that happen to share a
// name.
type Scope string

const (
	Global  Scope = "global"
	Project Scope = "project"
)

// projectDirName is the opt-in marker: a directory joins the scheme by having
// this path, which "snip scope init" creates. filepath.Join is a call rather
// than a const so the separator is right on every platform.
var projectDirName = filepath.Join(".claude", "snippets")

// projectStateDir holds one pair of state files per project, inside the user's
// own store. A project's enabled list lives here rather than in the repository
// so that no commit — and no clone — can change what a prompt carries. It also
// means a read-only checkout is still toggleable.
const projectStateDir = "projects"

// projectStatePath is the prefix for one project's state files. The leading
// slug keeps the directory readable by hand; the digest of the canonical root
// is what actually identifies the project, so two checkouts sharing a base name
// do not share state.
func projectStatePath(globalDir, root string) string {
	// Both halves come from the canonical root. Deriving the slug from the raw
	// path instead gave a symlinked checkout its own state file, even though
	// the digest beside it was identical — one repository, two enabled sets,
	// depending on which spelling you happened to cd through.
	resolved := canonical(root)
	sum := sha256.Sum256([]byte(resolved))
	return filepath.Join(globalDir, projectStateDir, projectSlug(resolved)+"-"+hex.EncodeToString(sum[:6]))
}

// projectSlug reduces a directory name to something safe on every filesystem.
// It is decoration: the digest beside it carries the identity.
func projectSlug(root string) string {
	out := make([]rune, 0, 24)
	for _, r := range strings.ToLower(filepath.Base(root)) {
		if len(out) == cap(out) {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		case len(out) > 0 && out[len(out)-1] != '-':
			out = append(out, '-')
		}
	}
	if slug := strings.Trim(string(out), "-"); slug != "" {
		return slug
	}
	return "project"
}

// Snippet identifies one snippet within one scope. Enabled state is captured
// when the Set is listed, so rendering N snippets reads each manifest once
// rather than once per row.
type Snippet struct {
	Name  string
	Scope Scope
	Store *Store
	on    bool
}

func (s Snippet) Enabled() bool   { return s.on }
func (s Snippet) Summary() string { return s.Store.Summary(s.Name) }

// Set is the global store plus, when the current directory opts in, a project
// store. Every command works against a Set so both scopes stay visible.
type Set struct {
	global  *Store
	project *Store // nil outside an opted-in directory
	root    string // the directory owning project, for display
}

func NewSet() (*Set, error) {
	g, err := NewStore()
	if err != nil {
		return nil, err
	}
	set := &Set{global: g}
	if root, dir, ok := findProjectDir(g.Dir()); ok {
		set.project = newProjectStoreAt(dir, projectStatePath(g.Dir(), root))
		set.root = root
	}
	return set, nil
}

// projectRootRefusal explains why a directory can never hold project snippets,
// and returns nil when it can. Two are refused: the configured global store,
// and $HOME itself. The global store lives at ~/.claude/snippets, which is
// exactly the marker's shape, so without these guards every directory under
// $HOME resolves to a project rooted at $HOME and writes meant for a project
// land in the global store instead.
//
// findProjectDir skips exactly what this refuses and "snip scope init" declines
// it, which is the point of having one predicate: init once created the marker
// wherever it was asked to, so in $HOME it reported success while every later
// command still resolved to the global scope.
func projectRootRefusal(dir, globalDir string) error {
	marker := filepath.Join(dir, projectDirName)
	if canonical(marker) == canonical(globalDir) {
		return fmt.Errorf("%s is the global snippet store, not a project; run snip scope init inside a repository instead", marker)
	}
	// $HOME/.claude is Claude Code's own configuration, never a project.
	if home, err := os.UserHomeDir(); err == nil && canonical(dir) == canonical(home) {
		return errors.New("$HOME is never a project root, since ~/.claude is Claude Code's own configuration; run snip scope init inside a repository instead")
	}
	return nil
}

// findProjectDir walks up from the working directory looking for the opt-in
// marker, stopping at the filesystem root. A marker in a directory
// projectRootRefusal rejects is not one.
func findProjectDir(globalDir string) (root, dir string, ok bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", false
	}
	for {
		candidate := filepath.Join(cwd, projectDirName)
		if st, err := os.Stat(candidate); err == nil && st.IsDir() &&
			projectRootRefusal(cwd, globalDir) == nil {
			return cwd, candidate, true
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			return "", "", false
		}
		cwd = parent
	}
}

// canonical resolves symlinks so two spellings of the same directory compare
// equal; it falls back to a cleaned path when the target does not exist.
func canonical(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

// Active is the store a mutating command targets: the project store when one
// exists, else global. The -g flag forces global.
func (s *Set) Active(forceGlobal bool) *Store {
	if forceGlobal || s.project == nil {
		return s.global
	}
	return s.project
}

func (s *Set) ActiveScope(forceGlobal bool) Scope {
	if forceGlobal || s.project == nil {
		return Global
	}
	return Project
}

// List returns every snippet in both scopes, global first, each group sorted
// by name.
func (s *Set) List() ([]Snippet, error) {
	var out []Snippet
	for _, store := range s.stores() {
		names, err := store.Names()
		if err != nil {
			return nil, err
		}
		enabled, err := store.Enabled()
		if err != nil {
			return nil, err
		}
		on := make(map[string]bool, len(enabled))
		for _, name := range enabled {
			on[name] = true
		}
		scope := s.scopeOf(store)
		for _, name := range names {
			out = append(out, Snippet{Name: name, Scope: scope, Store: store, on: on[name]})
		}
	}
	return out, nil
}

func (s *Set) scopeOf(store *Store) Scope {
	if store == s.project {
		return Project
	}
	return Global
}

// Resolve finds the store owning a bare name. The project scope wins when both
// define it, which is why -g exists.
func (s *Set) Resolve(name string, forceGlobal bool) (*Store, Scope, error) {
	if forceGlobal {
		if s.global.Exists(name) {
			return s.global, Global, nil
		}
		return nil, "", s.unknown(name, true)
	}
	if s.project != nil && s.project.Exists(name) {
		return s.project, Project, nil
	}
	if s.global.Exists(name) {
		return s.global, Global, nil
	}
	return nil, "", s.unknown(name, false)
}

func (s *Set) unknown(name string, globalOnly bool) error {
	all, _ := s.List()
	var known []string
	for _, sn := range all {
		if globalOnly && sn.Scope != Global {
			continue
		}
		known = append(known, sn.Name)
	}
	where := ""
	if globalOnly {
		where = " in the global scope"
	}
	if len(known) == 0 {
		return fmt.Errorf("no snippet %q%s", name, where)
	}
	return fmt.Errorf("no snippet %q%s (have: %s)", name, where, strings.Join(dedupe(known), ", "))
}

// Compose concatenates the enabled snippets of every scope into the text a
// prompt receives.
func (s *Set) Compose() (string, error) {
	var parts []string
	for _, store := range s.stores() {
		text, err := store.Compose()
		if err != nil {
			return "", err
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// Suggested is what the project scope proposes and the user has not decided
// on. Nothing outside a project ever suggests anything.
func (s *Set) Suggested() ([]string, error) {
	if s.project == nil {
		return nil, nil
	}
	return s.project.Suggested()
}

// EnabledLabels lists what is on, tagging a name only when it is ambiguous or
// project-owned, so the common case stays terse.
func (s *Set) EnabledLabels() ([]string, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	dupes := map[string]int{}
	for _, sn := range all {
		dupes[sn.Name]++
	}
	var out []string
	for _, sn := range all {
		if !sn.Enabled() {
			continue
		}
		if dupes[sn.Name] > 1 {
			out = append(out, fmt.Sprintf("%s(%s)", sn.Name, sn.Scope[:1]))
			continue
		}
		out = append(out, sn.Name)
	}
	return out, nil
}

// stores lists the active scopes in application order: global first, then
// project, so project instructions read as refinements on the baseline.
func (s *Set) stores() []*Store {
	if s.project == nil {
		return []*Store{s.global}
	}
	return []*Store{s.global, s.project}
}

// dedupe removes repeats while preserving first-seen order, which slices.Compact
// cannot do because it only collapses adjacent duplicates.
func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
