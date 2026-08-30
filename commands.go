package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alecthomas/kong"
)

type toggleCmd struct {
	Names []string `arg:"" optional:"" help:"Snippets to toggle. With none, list instead."`
}

// Run is the default command: `snip brief` toggles, bare `snip` lists.
func (c *toggleCmd) Run(ctx *Context) error {
	if len(c.Names) == 0 {
		return renderList(ctx.Set)
	}
	return eachResolved(ctx, c.Names, func(store *Store, names []string) error {
		for _, name := range names {
			if _, err := store.Toggle(name); err != nil {
				return err
			}
		}
		return nil
	})
}

type listCmd struct{}

func (*listCmd) Run(ctx *Context) error { return renderList(ctx.Set) }

type onCmd struct {
	Names []string `arg:"" help:"Snippets to enable."`
}

func (c *onCmd) Run(ctx *Context) error {
	return eachResolved(ctx, c.Names, func(store *Store, names []string) error {
		return store.Enable(names...)
	})
}

type offCmd struct {
	Names []string `arg:"" help:"Snippets to disable."`
}

func (c *offCmd) Run(ctx *Context) error {
	return eachResolved(ctx, c.Names, func(store *Store, names []string) error {
		return store.Disable(names...)
	})
}

type onlyCmd struct {
	Names []string `arg:"" optional:"" help:"The complete enabled set for the active scope."`
}

// Run replaces the active scope's manifest, leaving the other scope alone.
func (c *onlyCmd) Run(ctx *Context) error {
	store := ctx.Store()
	for _, name := range c.Names {
		if !store.Exists(name) {
			return fmt.Errorf("no snippet %q in the %s scope", name, ctx.Scope())
		}
	}
	if err := store.SetEnabled(c.Names); err != nil {
		return err
	}
	return renderList(ctx.Set)
}

type clearCmd struct{}

func (*clearCmd) Run(ctx *Context) error {
	if err := ctx.Store().SetEnabled(nil); err != nil {
		return err
	}
	return renderList(ctx.Set)
}

type showCmd struct {
	Names []string `arg:"" optional:"" help:"Snippets to print. With none, print all enabled text."`
}

// Run prints named snippets in full, or the composed text that actually
// reaches a prompt when given no names.
func (c *showCmd) Run(ctx *Context) error {
	if len(c.Names) == 0 {
		text, err := ctx.Set.Compose()
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
	for i, name := range c.Names {
		store, scope, err := ctx.Set.Resolve(name, ctx.Global)
		if err != nil {
			return err
		}
		body, err := store.Body(name)
		if err != nil {
			return err
		}
		if len(c.Names) > 1 {
			if i > 0 {
				fmt.Println()
			}
			fmt.Printf("%s%s%s %s(%s)%s\n", bold, name, reset, dim, scope, reset)
		}
		fmt.Println(body)
	}
	return nil
}

type addCmd struct {
	Name string   `arg:"" optional:"" help:"Snippet name. Prompted for when omitted."`
	Text []string `arg:"" optional:"" help:"Snippet body. Read from stdin when omitted."`
}

func (c *addCmd) Run(ctx *Context) error {
	store := ctx.Store()
	name, body := c.Name, strings.Join(c.Text, " ")

	if name == "" {
		if !onTerminal() {
			return fmt.Errorf("expected a snippet name")
		}
		var err error
		if name, err = promptLine("name: "); err != nil {
			return err
		}
		if name = strings.TrimSpace(name); name == "" {
			return fmt.Errorf("no name given")
		}
	}
	if strings.ContainsAny(name, `/\ `) {
		return fmt.Errorf("snippet names cannot contain spaces or slashes")
	}
	if store.Exists(name) {
		return fmt.Errorf("snippet %q already exists in the %s scope; edit it with: snip edit %s",
			name, ctx.Scope(), name)
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
	fmt.Printf("created %s\n", store.Path(name))
	return nil
}

type editCmd struct {
	Name string `arg:"" help:"Snippet to open."`
}

func (c *editCmd) Run(ctx *Context) error {
	store, _, err := ctx.Set.Resolve(c.Name, ctx.Global)
	if err != nil {
		return err
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, store.Path(c.Name))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

type rmCmd struct {
	Names []string `arg:"" help:"Snippets to delete."`
}

func (c *rmCmd) Run(ctx *Context) error {
	return eachResolved(ctx, c.Names, func(store *Store, names []string) error {
		return store.RemoveAll(names...)
	})
}

// scopeCmd has no Run of its own: kong invokes every Run along the selected
// path, so a Run here would also fire during `snip scope init` — and report a
// stale "project: none", since the Set was resolved before init created the
// directory. The report is a default subcommand instead.
type scopeCmd struct {
	Show scopeShowCmd `cmd:"" default:"1" help:"Report which scopes are active."`
	Init scopeInitCmd `cmd:"" help:"Opt this directory in to project snippets."`
}

type scopeShowCmd struct{}

func (*scopeShowCmd) Run(ctx *Context) error {
	fmt.Printf("global:  %s\n", ctx.Set.global.Dir())
	if ctx.Set.project == nil {
		fmt.Println("project: none; opt in with: snip scope init")
		return nil
	}
	fmt.Printf("project: %s\n", ctx.Set.project.Dir())
	fmt.Printf("         rooted at %s\n", ctx.Set.root)
	return nil
}

type scopeInitCmd struct{}

func (*scopeInitCmd) Run(ctx *Context) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	dir := filepath.Join(cwd, projectDirName)
	if ctx.Set.project != nil && ctx.Set.root == cwd {
		return fmt.Errorf("%s already opted in (%s)", cwd, dir)
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return err
	}
	fmt.Printf("created %s\n", dir)
	fmt.Println("project snippets now apply here; add one with: snip add <name> <text>")
	return nil
}

// helpCmd exists because --help is a flag, and `snip help` would otherwise
// fall through to the default toggle command and report "no snippet \"help\"".
type helpCmd struct {
	Command []string `arg:"" optional:"" help:"Command to describe. Omit for the top-level help."`
}

// Run delegates to the --help flag, which knows how to render every node.
// Tracing the arguments directly would not work: the default toggle command
// claims an empty path, so `snip help` would describe toggle rather than the
// application, and an unknown topic would silently become a snippet name.
func (c *helpCmd) Run(kctx *kong.Context) error {
	if len(c.Command) > 0 {
		if err := requireCommand(kctx, c.Command[0]); err != nil {
			return err
		}
	}
	_, err := kctx.Kong.Parse(append(c.Command, "--help"))
	return err
}

// requireCommand rejects a help topic that is not a command, so `snip help
// bogus` says so instead of printing something unrelated.
func requireCommand(kctx *kong.Context, name string) error {
	var known []string
	for _, node := range kctx.Kong.Model.Children {
		if node.Name == name || slices.Contains(node.Aliases, name) {
			return nil
		}
		if !node.Hidden {
			known = append(known, node.Name)
		}
	}
	return fmt.Errorf("no command %q (have: %s)", name, strings.Join(known, ", "))
}

type hookCmd struct{}

// hookOutput is the JSON contract of a UserPromptSubmit hook. suppressOutput
// keeps the raw stdout out of the transcript; AdditionalContext is what
// reaches the model.
type hookOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
	SuppressOutput bool `json:"suppressOutput"`
}

// Run stays silent when nothing is enabled, which costs the prompt nothing.
func (*hookCmd) Run(ctx *Context) error {
	text, err := ctx.Set.Compose()
	if err != nil || strings.TrimSpace(text) == "" {
		return err
	}
	var out hookOutput
	out.HookSpecificOutput.HookEventName = "UserPromptSubmit"
	out.HookSpecificOutput.AdditionalContext = text
	out.SuppressOutput = true
	return json.NewEncoder(os.Stdout).Encode(out)
}

type statusCmd struct{}

func (*statusCmd) Run(ctx *Context) error {
	labels, err := ctx.Set.EnabledLabels()
	if err != nil || len(labels) == 0 {
		fmt.Print("snip: none")
		return err
	}
	fmt.Printf("snip: %s", strings.Join(labels, ","))
	return nil
}

type menuCmd struct{}

// menuRow is what the /snip slash command consumes. It is JSON rather than a
// delimited line because a summary may contain any separator character.
type menuRow struct {
	Name    string `json:"name"`
	Scope   Scope  `json:"scope"`
	Enabled bool   `json:"enabled"`
	Summary string `json:"summary"`
}

func (*menuCmd) Run(ctx *Context) error {
	all, err := ctx.Set.List()
	if err != nil {
		return err
	}
	rows := make([]menuRow, 0, len(all))
	for _, sn := range all {
		rows = append(rows, menuRow{
			Name:    sn.Name,
			Scope:   sn.Scope,
			Enabled: sn.Enabled(),
			Summary: sn.Summary(),
		})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}

// eachResolved resolves every name to its owning store before mutating
// anything, so a typo in the third argument leaves the first two unapplied,
// then prints the resulting list.
func eachResolved(ctx *Context, names []string, apply func(*Store, []string) error) error {
	byStore := map[*Store][]string{}
	var order []*Store
	for _, name := range names {
		store, _, err := ctx.Set.Resolve(name, ctx.Global)
		if err != nil {
			return err
		}
		if _, seen := byStore[store]; !seen {
			order = append(order, store)
		}
		byStore[store] = append(byStore[store], name)
	}
	for _, store := range order {
		if err := apply(store, byStore[store]); err != nil {
			return err
		}
	}
	return renderList(ctx.Set)
}

// onTerminal reports whether stdin is an interactive terminal, so `snip add`
// prompts for a human and stays pipe-friendly otherwise. The mode bits alone
// are not enough: /dev/null is also a character device, so this probes with a
// terminal-only ioctl.
func onTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	probe := exec.Command("stty", "-g")
	probe.Stdin = os.Stdin
	return probe.Run() == nil
}

func promptLine(label string) (string, error) {
	fmt.Fprint(os.Stderr, label)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
