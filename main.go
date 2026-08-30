// Command snip toggles prompt snippets that a Claude Code UserPromptSubmit
// hook appends to every prompt.
//
// Snippets live in two scopes: global (~/.claude/snippets) and, for a
// directory that opted in with "snip scope init", project
// (<root>/.claude/snippets). Both scopes apply. A bare name resolves to the
// project snippet when both scopes define it; -g forces global.
package main

import "github.com/alecthomas/kong"

// CLI is the whole command surface. Kong derives parsing, help and completion
// from these tags, so the structure below is the specification.
type CLI struct {
	Global  bool             `short:"g" help:"Act on the global scope even inside a project."`
	Version kong.VersionFlag `short:"v" help:"Print the version and exit."`

	Toggle toggleCmd `cmd:"" default:"withargs" help:"Toggle snippets, or list them when given no names."`
	List   listCmd   `cmd:"" aliases:"ls" help:"List snippets in both scopes."`
	On     onCmd     `cmd:"" help:"Enable snippets."`
	Off    offCmd    `cmd:"" help:"Disable snippets."`
	Only   onlyCmd   `cmd:"" help:"Enable exactly these snippets in the active scope."`
	Clear  clearCmd  `cmd:"" help:"Disable every snippet in the active scope."`
	Pick   pickCmd   `cmd:"" help:"Choose snippets in an interactive checkbox list."`
	Show   showCmd   `cmd:"" aliases:"cat,view" help:"Print snippets in full, or all enabled text."`
	Add    addCmd    `cmd:"" aliases:"new,create" help:"Create a snippet."`
	Edit   editCmd   `cmd:"" help:"Open a snippet in $EDITOR."`
	Rm     rmCmd     `cmd:"" aliases:"remove,delete" help:"Delete snippets."`
	Scope  scopeCmd  `cmd:"" help:"Show or set up snippet scopes."`
	Help   helpCmd   `cmd:"" help:"Show help for a command."`

	Hook   hookCmd   `cmd:"" hidden:"" help:"Emit UserPromptSubmit JSON."`
	Status statusCmd `cmd:"" hidden:"" help:"Print a one-line summary for the status line."`
	Menu   menuCmd   `cmd:"" hidden:"" help:"Print a JSON inventory."`
}

// Context carries what every command needs: the resolved scopes and whether
// -g was given. Kong binds it into each Run method.
type Context struct {
	Set    *Set
	Global bool
}

// Store is the scope a mutating command targets, and Scope names it.
func (c *Context) Store() *Store { return c.Set.Active(c.Global) }
func (c *Context) Scope() Scope  { return c.Set.ActiveScope(c.Global) }

func main() {
	var cli CLI
	kctx := kong.Parse(&cli,
		kong.Name("snip"),
		kong.Description(description),
		kong.UsageOnError(),
		kong.Vars{"version": version},
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
	)

	set, err := NewSet()
	if err != nil {
		kctx.FatalIfErrorf(err)
	}
	kctx.FatalIfErrorf(kctx.Run(&Context{Set: set, Global: cli.Global}))
}

const description = `Toggle instruction snippets that get appended to every Claude Code prompt.

Snippets live in two scopes, and both apply: global (~/.claude/snippets, or
$SNIP_DIR) and project (<root>/.claude/snippets, created by "snip scope init").
A prompt receives every enabled global snippet, then every enabled project one.

When both scopes define the same name, a bare reference means the project
snippet and -g means the global one. Both can be enabled at the same time.`
