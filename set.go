package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Scope names where a snippet lives. Project snippets shadow global ones of
// the same name for bare-name lookups, but both remain listable and enablable
// independently — the two files are separate snippets that happen to share a
// name.
type Scope string

const (
	Global  Scope = "global"
	Project Scope = "project"
)

// projectDirName is the opt-in marker: a repository joins the scheme by having
// this directory, which `snip scope init` creates.
var projectDirName = filepath.Join(".claude", "snippets")

// Snippet identifies one snippet within one scope.
type Snippet struct {
	Name  string
	Scope Scope
	Store *Store
}

func (s Snippet) Enabled() bool   { return s.Store.IsEnabled(s.Name) }
func (s Snippet) Summary() string { return s.Store.Summary(s.Name) }

// Set is the global store plus, when the current directory opts in, a project
// store. Every command works against a Set so both scopes stay visible.
type Set struct {
	global  *Store
	project *Store // nil outside an opted-in directory
	Root    string // the directory owning project, for display
}

func NewSet() (*Set, error) {
	g, err := NewStore()
	if err != nil {
		return nil, err
	}
	set := &Set{global: g}
	if root, dir, ok := findProjectDir(g.Dir); ok {
		set.project = &Store{Dir: dir}
		set.Root = root
	}
	return set, nil
}

// findProjectDir walks up from the working directory looking for the opt-in
// marker, stopping at the filesystem root.
//
// Two directories are never project roots: the configured global store, and
// $HOME itself. The global store lives at ~/.claude/snippets, which is exactly
// the marker's shape, so without these guards every directory under $HOME
// resolves to a project rooted at $HOME and writes meant for a project land in
// the global store instead.
func findProjectDir(globalDir string) (root, dir string, ok bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", false
	}
	global := canonical(globalDir)
	home := ""
	if h, err := os.UserHomeDir(); err == nil {
		home = canonical(h)
	}
	for {
		candidate := filepath.Join(cwd, projectDirName)
		// $HOME/.claude is Claude Code's own configuration, never a project.
		skip := canonical(candidate) == global || (home != "" && canonical(cwd) == home)
		if st, err := os.Stat(candidate); err == nil && st.IsDir() && !skip {
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

// List returns every snippet in both scopes: global first, then project, each
// group sorted by name.
func (s *Set) List() ([]Snippet, error) {
	var out []Snippet
	for _, pair := range []struct {
		scope Scope
		store *Store
	}{{Global, s.global}, {Project, s.project}} {
		if pair.store == nil {
			continue
		}
		names, err := pair.store.Names()
		if err != nil {
			return nil, err
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, Snippet{Name: n, Scope: pair.scope, Store: pair.store})
		}
	}
	return out, nil
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
		known = append(known, string(sn.Name))
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

// Compose concatenates every enabled snippet: global first so a project
// snippet's instructions read as refinements on top of the baseline.
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

func (s *Set) stores() []*Store {
	out := []*Store{s.global}
	if s.project != nil {
		out = append(out, s.project)
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
