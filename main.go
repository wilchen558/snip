// snip toggles prompt snippets that a Claude Code UserPromptSubmit hook
// appends to every prompt. One binary serves the CLI, the picker, the hook
// itself and the status line.
//
// Snippets live in two scopes: global (~/.claude/snippets) and, for a
// directory that opted in with `snip scope init`, project
// (<root>/.claude/snippets). Both are listed and both apply; a bare name
// resolves to the project one when both scopes define it, and -g forces
// global.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const usage = `snip — prompt snippets appended to every Claude Code prompt

  snip                     list snippets in both scopes, [x] = enabled
  snip <name>...           toggle
  snip on <name>...        enable
  snip off <name>...       disable
  snip only <name>...      enable exactly these in the active scope
  snip clear               disable everything in the active scope
  snip pick                interactive checkbox picker (needs a real terminal)
  snip show [name...]      print a snippet in full, or all enabled text
  snip add <name> [text]   create a snippet (prompts, or reads stdin, if omitted)
  snip edit <name>         open a snippet in $EDITOR
  snip rm <name>           delete a snippet

  snip scope               report which scopes are active
  snip scope init          opt this directory in to project snippets

  snip hook                emit UserPromptSubmit JSON (used by the hook)
  snip status              one-line summary (used by the status line)
  snip menu                machine-readable JSON inventory

Flags
  -g, --global             act on the global scope even inside a project

A project snippet shadows a global one of the same name for bare-name
lookups; both still appear in the list and both can be enabled at once.`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "snip: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	// A leading -g applies to whichever subcommand follows.
	global := false
	for len(args) > 0 && (args[0] == "-g" || args[0] == "--global") {
		global = true
		args = args[1:]
	}

	set, err := NewSet()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return cmdList(set)
	}

	switch args[0] {
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	case "version", "-v", "--version":
		fmt.Println("snip " + version)
		return nil
	case "list", "ls":
		return cmdList(set)
	case "on":
		return applyKnown(set, args[1:], global, func(st *Store, n []string) error { return st.Enable(n...) })
	case "off":
		return applyKnown(set, args[1:], global, func(st *Store, n []string) error { return st.Disable(n...) })
	case "only":
		return cmdOnly(set, args[1:], global)
	case "clear":
		if err := set.Active(global).SetEnabled(nil); err != nil {
			return err
		}
		return cmdList(set)
	case "pick":
		return cmdPick(set)
	case "show", "cat", "view":
		return cmdShow(set, args[1:], global)
	case "new", "add", "create":
		return cmdAdd(set, args[1:], global)
	case "edit":
		return cmdEdit(set, args[1:], global)
	case "rm", "remove", "delete":
		return applyKnown(set, args[1:], global, func(st *Store, n []string) error { return st.RemoveAll(n...) })
	case "scope":
		return cmdScope(set, args[1:])
	case "hook":
		return cmdHook(set)
	case "status":
		return cmdStatus(set)
	case "menu":
		return cmdMenu(set)
	}

	// No subcommand matched, so every argument is a snippet to toggle. This is
	// the common path: `snip brief`.
	return applyKnown(set, args, global, func(st *Store, names []string) error {
		for _, n := range names {
			if _, err := st.Toggle(n); err != nil {
				return err
			}
		}
		return nil
	})
}

func cmdList(set *Set) error {
	all, err := set.List()
	if err != nil {
		return err
	}
	if len(all) == 0 {
		fmt.Printf("no snippets in %s — create one with: snip add <name> <text>\n", set.global.Dir)
		return nil
	}
	tagged := set.project != nil
	for _, sn := range all {
		mark := " "
		if sn.Enabled() {
			mark = "x"
		}
		scope := ""
		if tagged {
			scope = fmt.Sprintf(" \033[2m(%s)\033[0m", sn.Scope)
		}
		fmt.Printf("  [%s] %-16s%s %s\n", mark, sn.Name, scope, truncate(sn.Summary(), 44))
	}
	if tagged {
		fmt.Printf("\033[2m  project: %s\033[0m\n", set.Root)
	}
	return nil
}

func cmdOnly(set *Set, names []string, global bool) error {
	store := set.Active(global)
	for _, n := range names {
		if !store.Exists(n) {
			return fmt.Errorf("no snippet %q in the %s scope", n, set.ActiveScope(global))
		}
	}
	if err := store.SetEnabled(names); err != nil {
		return err
	}
	return cmdList(set)
}

func cmdPick(set *Set) error {
	all, err := set.List()
	if err != nil {
		return err
	}
	if len(all) == 0 {
		return fmt.Errorf("no snippets in %s", set.global.Dir)
	}
	items := make([]item, 0, len(all))
	for _, sn := range all {
		label := sn.Name
		if set.project != nil {
			label = fmt.Sprintf("%s (%s)", sn.Name, sn.Scope)
		}
		items = append(items, item{name: label, summary: sn.Summary(), on: sn.Enabled()})
	}
	chosen, ok, err := Pick(items)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("cancelled")
		return nil
	}
	// Map the picked labels back to per-store selections.
	picked := map[string]bool{}
	for _, c := range chosen {
		picked[c] = true
	}
	perStore := map[*Store][]string{}
	for i, sn := range all {
		if picked[items[i].name] {
			perStore[sn.Store] = append(perStore[sn.Store], sn.Name)
		}
	}
	for _, store := range set.stores() {
		if err := store.SetEnabled(perStore[store]); err != nil {
			return err
		}
	}
	return cmdList(set)
}

// cmdShow prints named snippets in full. With no names it falls back to the
// composed text of everything enabled — what actually reaches a prompt.
func cmdShow(set *Set, names []string, global bool) error {
	if len(names) == 0 {
		text, err := set.Compose()
		if err != nil {
			return err
		}
		if text == "" {
			fmt.Println("(nothing enabled)")
			return nil
		}
		fmt.Println(text)
		return nil
	}
	for i, n := range names {
		store, scope, err := set.Resolve(n, global)
		if err != nil {
			return err
		}
		body, err := store.Body(n)
		if err != nil {
			return err
		}
		if len(names) > 1 {
			if i > 0 {
				fmt.Println()
			}
			fmt.Printf("\033[1m%s\033[0m \033[2m(%s)\033[0m\n", n, scope)
		}
		fmt.Println(body)
	}
	return nil
}

func cmdAdd(set *Set, args []string, global bool) error {
	store := set.Active(global)
	var name, body string
	if len(args) == 0 {
		if !onTerminal() {
			return fmt.Errorf("usage: snip add <name> [text...]")
		}
		var err error
		if name, err = promptLine("name: "); err != nil {
			return err
		}
		if name = strings.TrimSpace(name); name == "" {
			return fmt.Errorf("no name given")
		}
	} else {
		name = args[0]
		body = strings.Join(args[1:], " ")
	}
	if strings.ContainsAny(name, `/\ `) {
		return fmt.Errorf("snippet names cannot contain spaces or slashes")
	}
	if store.Exists(name) {
		return fmt.Errorf("snippet %q already exists in the %s scope — edit it with: snip edit %s",
			name, set.ActiveScope(global), name)
	}
	if body == "" {
		if onTerminal() {
			fmt.Fprintln(os.Stderr, "snippet text (finish with ctrl-d):")
		}
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		body = string(b)
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("refusing to create an empty snippet")
	}
	if err := store.Write(name, body); err != nil {
		return err
	}
	fmt.Printf("created %s\n", store.path(name))
	return nil
}

func cmdEdit(set *Set, args []string, global bool) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: snip edit <name>")
	}
	store, _, err := set.Resolve(args[0], global)
	if err != nil {
		return err
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, store.path(args[0]))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func cmdScope(set *Set, args []string) error {
	if len(args) > 0 && args[0] == "init" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		dir := filepath.Join(cwd, projectDirName)
		if set.project != nil && set.Root == cwd {
			return fmt.Errorf("%s already opted in (%s)", cwd, dir)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		fmt.Printf("created %s\n", dir)
		fmt.Println("project snippets now apply here; add one with: snip add <name> <text>")
		return nil
	}
	fmt.Printf("global:  %s\n", set.global.Dir)
	if set.project == nil {
		fmt.Println("project: none — opt in with: snip scope init")
		return nil
	}
	fmt.Printf("project: %s\n", set.project.Dir)
	fmt.Printf("         rooted at %s\n", set.Root)
	return nil
}

// cmdHook emits the JSON a UserPromptSubmit hook returns. suppressOutput keeps
// the raw stdout out of the transcript; additionalContext is what reaches the
// model. Exiting silently when nothing is enabled costs the prompt nothing.
func cmdHook(set *Set) error {
	text, err := set.Compose()
	if err != nil || strings.TrimSpace(text) == "" {
		return nil
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":     "UserPromptSubmit",
			"additionalContext": text,
		},
		"suppressOutput": true,
	})
}

func cmdStatus(set *Set) error {
	on, err := set.EnabledLabels()
	if err != nil || len(on) == 0 {
		fmt.Print("snip: none")
		return nil
	}
	fmt.Printf("snip: %s", strings.Join(on, ","))
	return nil
}

// cmdMenu feeds the /snip slash command. It emits JSON rather than a delimited
// line because a snippet summary may itself contain any separator character.
func cmdMenu(set *Set) error {
	all, err := set.List()
	if err != nil {
		return err
	}
	type row struct {
		Name    string `json:"name"`
		Scope   string `json:"scope"`
		Enabled bool   `json:"enabled"`
		Summary string `json:"summary"`
	}
	rows := make([]row, 0, len(all))
	for _, sn := range all {
		rows = append(rows, row{Name: sn.Name, Scope: string(sn.Scope), Enabled: sn.Enabled(), Summary: sn.Summary()})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}

// applyKnown resolves every name to its owning store before mutating anything,
// so a typo in the third argument does not leave the first two applied.
func applyKnown(set *Set, names []string, global bool, fn func(*Store, []string) error) error {
	if len(names) == 0 {
		return fmt.Errorf("expected at least one snippet name")
	}
	byStore := map[*Store][]string{}
	var order []*Store
	for _, n := range names {
		store, _, err := set.Resolve(n, global)
		if err != nil {
			return err
		}
		if _, seen := byStore[store]; !seen {
			order = append(order, store)
		}
		byStore[store] = append(byStore[store], n)
	}
	for _, store := range order {
		if err := fn(store, byStore[store]); err != nil {
			return err
		}
	}
	return cmdList(set)
}

// onTerminal reports whether stdin is an interactive terminal, so `snip add`
// can prompt when a human runs it and stay pipe-friendly otherwise. The mode
// bits alone are not enough: /dev/null is also a character device, so this
// probes with a terminal-only ioctl.
func onTerminal() bool {
	st, err := os.Stdin.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	probe := exec.Command("stty", "-g")
	probe.Stdin = os.Stdin
	return probe.Run() == nil
}

func promptLine(label string) (string, error) {
	fmt.Fprint(os.Stderr, label)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
