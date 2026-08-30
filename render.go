package main

import (
	"fmt"
	"strings"
)

// ANSI escapes used across the CLI and the picker.
const (
	reset = "\033[0m"
	bold  = "\033[1m"
	dim   = "\033[2m"
	cyan  = "\033[36m"
)

// renderList prints every snippet in both scopes. Scope tags appear only when
// a project scope is active, so the common single-scope output stays terse.
func renderList(set *Set) error {
	all, err := set.List()
	if err != nil {
		return err
	}
	if len(all) == 0 {
		fmt.Printf("no snippets in %s; create one with: snip add <name> <text>\n", set.global.Dir())
		return nil
	}
	tagged := set.project != nil
	for _, sn := range all {
		mark := ' '
		if sn.Enabled() {
			mark = 'x'
		}
		scope := ""
		if tagged {
			scope = fmt.Sprintf(" %s(%s)%s", dim, sn.Scope, reset)
		}
		fmt.Printf("  [%c] %-16s%s %s\n", mark, sn.Name, scope, truncate(sn.Summary(), 44))
	}
	if !tagged {
		return nil
	}
	fmt.Printf("%s  project: %s%s\n", dim, set.root, reset)
	// A repository proposes; it never applies. Surfacing the proposal here is
	// what makes that safe rather than merely silent — the snippets are visible
	// in the list above, and this is the line that says how to turn them on.
	suggested, err := set.Suggested()
	if err != nil {
		return err
	}
	if len(suggested) > 0 {
		fmt.Printf("%s  project suggests: %s%s\n", dim, strings.Join(suggested, ", "), reset)
		fmt.Printf("%s  adopt with: snip scope adopt%s\n", dim, reset)
	}
	return nil
}

// truncate shortens s to at most max runes, marking any elision.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}
