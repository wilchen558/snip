# AGENTS.md

Guidance for coding agents working in this repository. Human-facing usage lives
in [README.md](README.md); this file covers how the code is arranged, what to
run, and the traps that have already bitten once.

## What this is

`snip` toggles instruction snippets that a Claude Code `UserPromptSubmit` hook
appends to every prompt. The same binary serves four callers:

| Invocation | Caller | Contract |
| --- | --- | --- |
| `snip hook` | the `UserPromptSubmit` hook | JSON on stdout, or **nothing** when no snippets are enabled |
| `snip status` | the `statusLine` command | a single line, no trailing newline |
| `snip menu` | the `/snip` slash command | a JSON array |
| everything else | a human at a terminal | human-readable |

Changing the shape of the first three breaks a live integration in the user's
`~/.claude/settings.json`. Treat them as public API.

## Commands

```sh
go build -o /tmp/snip .                                    # build
go test ./...                                              # 26 tests, ~30ms
go test -run TestConcurrent -count=5 ./...                 # the flaky-prone one
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@latest ./...     # must stay clean
gofmt -l .                                                 # must print nothing
```

Install for real use:

```sh
go build -ldflags "-X main.version=$(git describe --tags --always)" -o ~/.local/bin/snip .
```

`-race` needs `CGO_ENABLED=1` and a C toolchain; the concurrency test is
deterministic without it, so plain `go test` is the normal path.

## Layout

| File | Role |
| --- | --- |
| `main.go` | the `CLI` struct. Kong derives parsing, help and aliases from its tags — this is the command surface, read it first |
| `commands.go` | one `Run(ctx *Context) error` method per command |
| `set.go` | scope resolution: which store owns a name, what a prompt receives |
| `snippets.go` | one scope's on-disk store: snippet files plus the `.enabled` manifest, with locking |
| `picker.go` | raw-mode checkbox TUI and the `pick` command |
| `render.go` | shared list rendering and ANSI constants |
| `version.go` | `version`, set via `-ldflags` |

The dependency direction is one-way: `commands.go` → `set.go` → `snippets.go`.
Nothing below `commands.go` prints to stdout except `render.go`.

## Core model

Two **scopes**, both of which apply to a prompt:

- **global** — `~/.claude/snippets/`, overridable with `SNIP_DIR`
- **project** — `<root>/.claude/snippets/`, created by `snip scope init`,
  discovered by walking up from the working directory

`Store` is one scope's directory. `Set` is global plus an optional project
store, and is what commands operate on. `Set.Active(global bool)` picks the
store a mutation targets: project when one exists, global otherwise, and always
global when `-g` was passed.

Name resolution (`Set.Resolve`) prefers the project scope, which is why `-g`
exists. Two scopes may define the same name; they are separate snippets and can
both be enabled.

Composition order is global first, then project, so project instructions read as
refinements on the baseline. That order is asserted by
`TestComposePutsGlobalFirstThenProject` — do not change it casually.

## Traps

These are real bugs that were shipped and fixed. The regression tests exist;
please do not reintroduce the causes.

1. **`$HOME` is not a project root.** The global store lives at
   `~/.claude/snippets`, which is byte-for-byte the shape of the project marker.
   Without the guard in `findProjectDir`, every directory under `$HOME` resolves
   to a project rooted at `$HOME`, and writes intended for a project land in the
   user's global store. Both the configured global directory and `$HOME` are
   excluded. Guarded by `TestFindProjectDirSkipsTheGlobalStore`.

2. **Manifest writes must go through `Store.mutate`.** It takes an advisory lock
   (exclusive file creation, stale after 30s) and writes atomically via
   temp-file rename. A plain read-modify-write loses roughly half of twelve
   concurrent toggles, which matters because several Claude Code sessions run at
   once. Guarded by `TestConcurrentEnableKeepsEveryUpdate`.

3. **The picker must clamp to terminal height.** It repositions the cursor with
   a relative `\033[NA`, so a frame taller than the window scrolls the terminal
   and desyncs the math. `Pick` scrolls a window sized from `stty size`.

4. **Machine-readable output must not be delimiter-separated.** `snip menu` once
   emitted `name|state|summary`; a snippet whose text contained a pipe broke the
   consumer. It emits JSON now. Same reasoning applies to anything new.

5. **Terminal detection needs an ioctl, not mode bits.** `/dev/null` is also a
   character device, so `os.ModeCharDevice` alone reports a piped stdin as
   interactive. `onTerminal` probes with `stty -g`.

6. **Snippet names must go through `validName`.** A name becomes a file name
   directly, so an unvalidated one escapes the store: `snip rm ../victim/notes`
   deleted a file outside the snippet directory. `Store.Exists` returns false
   for an invalid name, which makes every lookup path inherit the guard, and
   `Write`, `Body` and `RemoveAll` reject one outright. Guarded by
   `TestNamesCannotEscapeTheStore`.

7. **Kong runs every `Run` along the selected path.** A parent command with
   both a `Run` method and subcommands fires that `Run` during the subcommand
   too — `snip scope init` printed a stale scope report because the `Set` was
   resolved before `init` created the directory. Give the parent no `Run` and
   add a `default:"1"` subcommand for its bare form, as `scopeCmd` does.

8. **A snippet named after a subcommand** (`on`, `show`, `list`) cannot be
   toggled by bare name — kong claims the token. `snip toggle on` is the
   documented escape hatch. Adding a subcommand shadows that name, so weigh new
   subcommands against likely snippet names.

## Conventions

- Everything is `package main`. Types and the methods that form a type's API are
  exported for readability; struct fields stay unexported.
- Commands never reach into `Store` directly for resolution — go through
  `Set`, so both scopes are honoured.
- Mutating commands print the resulting list via `renderList`, so the user always
  sees the new state. `eachResolved` resolves every name before applying
  anything, so a typo in the third argument leaves the first two untouched.
- Error strings are lowercase and unpunctuated; use `%w` when a caller could
  reasonably inspect the cause.
- Comments explain *why*. The tests carry the *what*.
- Tests point `SNIP_DIR` at `t.TempDir()` and use `t.Chdir` for scope
  discovery. Never let a test touch a real `~/.claude`.

## Adding a command

1. Add a field to `CLI` in `main.go` with a `cmd:""` tag and help text.
2. Add the type and its `Run(ctx *Context) error` to `commands.go`.
3. Use `ctx.Store()` / `ctx.Scope()` for the active scope, or
   `ctx.Set.Resolve(name, ctx.Global)` to look a name up across both.
4. End mutating commands with `renderList(ctx.Set)`.
5. Add a test at the `Set` or `Store` level — the `Run` methods are thin on
   purpose, and the logic worth testing sits below them.
6. Update the command reference table in `README.md`.

## Safety

The tool writes to the user's live `~/.claude/snippets`. When testing by hand,
export `SNIP_DIR` to a scratch directory first, and remember that doing so does
**not** disable project discovery — `cd` somewhere without a `.claude/snippets`
ancestor as well.
