// snip toggles prompt snippets that a Claude Code UserPromptSubmit hook
// appends to every prompt. One binary serves the CLI, the picker, the hook
// itself and the status line.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

const usage = `snip — prompt snippets appended to every Claude Code prompt

  snip                     list snippets, [x] = enabled
  snip <name>...           toggle those snippets
  snip on <name>...        enable
  snip off <name>...       disable
  snip only <name>...      enable exactly these, disable the rest
  snip clear               disable everything
  snip pick                interactive checkbox picker (needs a real terminal)
  snip show [name...]      print a snippet in full, or all enabled text if no name
  snip add <name> [text]   create a snippet (prompts, or reads stdin, when text is omitted)
  snip edit <name>         open a snippet in $EDITOR
  snip rm <name>           delete a snippet

  snip hook                emit UserPromptSubmit JSON (used by the hook)
  snip status              one-line summary (used by the status line)
  snip menu                machine-readable JSON inventory`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "snip: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	s, err := NewStore()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return cmdList(s)
	}

	switch args[0] {
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	case "list", "ls":
		return cmdList(s)
	case "on":
		return applyKnown(s, args[1:], s.Enable)
	case "off":
		return applyKnown(s, args[1:], s.Disable)
	case "only":
		if err := requireKnown(s, args[1:]); err != nil {
			return err
		}
		if err := s.SetEnabled(args[1:]); err != nil {
			return err
		}
		return cmdList(s)
	case "clear":
		if err := s.SetEnabled(nil); err != nil {
			return err
		}
		return cmdList(s)
	case "pick":
		return cmdPick(s)
	case "show", "cat", "view":
		return cmdShow(s, args[1:])
	case "new", "add", "create":
		return cmdNew(s, args[1:])
	case "edit":
		return cmdEdit(s, args[1:])
	case "rm", "remove", "delete":
		return applyKnown(s, args[1:], s.RemoveAll)
	case "hook":
		return cmdHook(s)
	case "status":
		return cmdStatus(s)
	case "menu":
		return cmdMenu(s)
	}

	// No subcommand matched, so treat every argument as a snippet to toggle.
	// This is the common path: `snip brief`.
	return cmdToggle(s, args)
}

func cmdList(s *Store) error {
	names, err := s.Names()
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Printf("no snippets in %s — create one with: snip new <name> <text>\n", s.Dir)
		return nil
	}
	for _, n := range names {
		mark := " "
		if s.IsEnabled(n) {
			mark = "x"
		}
		fmt.Printf("  [%s] %-16s %s\n", mark, n, truncate(s.Summary(n), 52))
	}
	return nil
}

func cmdToggle(s *Store, names []string) error {
	if err := requireKnown(s, names); err != nil {
		return err
	}
	for _, n := range names {
		if _, err := s.Toggle(n); err != nil {
			return err
		}
	}
	return cmdList(s)
}

func cmdPick(s *Store) error {
	names, err := s.Names()
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("no snippets in %s", s.Dir)
	}
	items := make([]item, 0, len(names))
	for _, n := range names {
		items = append(items, item{name: n, summary: s.Summary(n), on: s.IsEnabled(n)})
	}
	chosen, ok, err := Pick(items)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("cancelled")
		return nil
	}
	if err := s.SetEnabled(chosen); err != nil {
		return err
	}
	return cmdList(s)
}

// cmdShow prints named snippets in full. With no names it falls back to the
// composed text of everything enabled — what actually reaches a prompt.
func cmdShow(s *Store, names []string) error {
	if len(names) == 0 {
		text, err := s.Compose()
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
	if err := requireKnown(s, names); err != nil {
		return err
	}
	for i, n := range names {
		body, err := s.Body(n)
		if err != nil {
			return err
		}
		if len(names) > 1 {
			if i > 0 {
				fmt.Println()
			}
			state := "off"
			if s.IsEnabled(n) {
				state = "on"
			}
			fmt.Printf("\033[1m%s\033[0m \033[2m(%s)\033[0m\n", n, state)
		}
		fmt.Println(body)
	}
	return nil
}

func cmdNew(s *Store, args []string) error {
	var name, body string
	switch {
	case len(args) == 0:
		// Fully interactive: ask for the name too.
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
	default:
		name = args[0]
		body = strings.Join(args[1:], " ")
	}
	if strings.ContainsAny(name, "/\\ ") {
		return fmt.Errorf("snippet names cannot contain spaces or slashes")
	}
	if s.Exists(name) {
		return fmt.Errorf("snippet %q already exists — edit it with: snip edit %s", name, name)
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
	if err := s.Write(name, body); err != nil {
		return err
	}
	fmt.Printf("created %s\n", s.path(name))
	return nil
}

func cmdEdit(s *Store, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: snip edit <name>")
	}
	if !s.Exists(args[0]) {
		return unknown(s, args[0])
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, s.path(args[0]))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// cmdHook emits the JSON a UserPromptSubmit hook returns. suppressOutput keeps
// the raw stdout out of the transcript; additionalContext is what reaches the
// model. Exiting silently when nothing is enabled costs the prompt nothing.
func cmdHook(s *Store) error {
	text, err := s.Compose()
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

func cmdStatus(s *Store) error {
	on, err := s.Enabled()
	if err != nil || len(on) == 0 {
		fmt.Print("snip: none")
		return nil
	}
	fmt.Printf("snip: %s", strings.Join(on, ","))
	return nil
}

// cmdMenu feeds the /snip slash command. It emits JSON rather than a delimited
// line because a snippet summary may itself contain any separator character.
func cmdMenu(s *Store) error {
	names, err := s.Names()
	if err != nil {
		return err
	}
	type row struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
		Summary string `json:"summary"`
	}
	rows := make([]row, 0, len(names))
	for _, n := range names {
		rows = append(rows, row{Name: n, Enabled: s.IsEnabled(n), Summary: s.Summary(n)})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}

func applyKnown(s *Store, names []string, fn func(...string) error) error {
	if len(names) == 0 {
		return fmt.Errorf("expected at least one snippet name")
	}
	if err := requireKnown(s, names); err != nil {
		return err
	}
	if err := fn(names...); err != nil {
		return err
	}
	return cmdList(s)
}

func requireKnown(s *Store, names []string) error {
	for _, n := range names {
		if !s.Exists(n) {
			return unknown(s, n)
		}
	}
	return nil
}

func unknown(s *Store, name string) error {
	known, _ := s.Names()
	return fmt.Errorf("no snippet %q (have: %s)", name, strings.Join(known, ", "))
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
