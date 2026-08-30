# snip

Reusable instruction snippets, appended to every Claude Code prompt.

Write an instruction once — "answer in under five sentences", "never assume, ask
first" — then switch it on and off without retyping it. A `UserPromptSubmit`
hook appends whatever is enabled to each prompt you send.

```console
$ snip
  [ ] ask-questions    Never assume. If any part of this request is ambiguo…
  [x] brief            Skip the preamble. Answer in under five sentences un…
  [ ] no-new-deps      Do not add new dependencies. Solve it with what's al…

$ snip ask-questions          # toggle it on
  [x] ask-questions    Never assume. If any part of this request is ambiguo…
  [x] brief            Skip the preamble. Answer in under five sentences un…
  [ ] no-new-deps      Do not add new dependencies. Solve it with what's al…
```

One binary. [kong](https://github.com/alecthomas/kong) is the only dependency.

## Install

```sh
git clone https://github.com/twilchen/snip && cd snip
go build -ldflags "-X main.version=$(git describe --tags --always)" -o ~/.local/bin/snip .
```

Make sure `~/.local/bin` is on your `PATH`.

## Wire it into Claude Code

Add to `~/.claude/settings.json`:

```json
{
  "hooks": {
    "UserPromptSubmit": [
      { "hooks": [{ "type": "command", "command": "$HOME/.local/bin/snip hook", "timeout": 5 }] }
    ]
  },
  "statusLine": { "type": "command", "command": "$HOME/.local/bin/snip status" },
  "respondToBashCommands": false
}
```

Three separate things happen here:

- **`snip hook`** is the hook itself. It prints the enabled snippets as
  `additionalContext` JSON, and prints nothing at all when none are enabled.
- **`snip status`** renders `snip: brief,no-new-deps` in the status line, so you
  can always see what is active without spending a turn asking.
- **`respondToBashCommands: false`** makes `! snip brief`, typed in Claude Code's
  input box, toggle without costing a model turn. It applies to every `!`
  command, not just this one.

Restart Claude Code to pick up the status line; the hook takes effect
immediately.

## Everyday use

```sh
snip                        # list; [x] means enabled
snip brief                  # toggle one
snip brief no-new-deps      # toggle several
snip on brief               # enable, regardless of current state
snip off brief              # disable
snip only brief             # make this the exact enabled set
snip clear                  # disable everything
```

Inspect what is actually being sent:

```console
$ snip show brief
Skip the preamble. Answer in under five sentences unless I ask for depth.

$ snip show                 # every enabled snippet, joined as the prompt sees it
Skip the preamble. Answer in under five sentences unless I ask for depth.

Do not add new dependencies. Solve it with what's already in the project, or
tell me which dependency you'd need and why before adding it.
```

Manage the collection:

```sh
snip add hurry "Answer in under five sentences."   # inline body
snip add hurry                                     # prompts, ctrl-d to finish
echo "Cite file:line for every claim." | snip add cite
snip edit hurry                                    # opens $EDITOR
snip rm hurry
```

Pick interactively — arrow keys, space to toggle, enter to save:

```console
$ snip pick
  space toggle · a all · n none · enter save · q cancel
> [x] brief            Skip the preamble. Answer in under five sentences…
  [ ] no-new-deps      Do not add new dependencies. Solve it with what's…
  [x] test-first       Write the failing test before the implementation…
```

`snip pick` draws a full-screen widget and needs a real terminal. It will not
work from Claude Code's `!` input box, which provides no TTY.

## Per-project snippets

A repository can carry its own snippets alongside your global ones. Both apply.

```console
$ cd ~/projects/backend
$ snip scope init
created /home/you/projects/backend/.claude/snippets
project snippets now apply here; add one with: snip add <name> <text>

$ snip add ticket "Reference the Jira ticket ID in every commit message."
created /home/you/projects/backend/.claude/snippets/ticket.md

$ snip ticket                 # enable it, for this repo only
  [x] brief        (global)   Skip the preamble. Answer in under five sen…
  [ ] no-new-deps  (global)   Do not add new dependencies. Solve it with …
  [x] ticket       (project)  Reference the Jira ticket ID in every commi…
  project: /home/you/projects/backend
```

A prompt sent from that repo now carries `brief` **and** `ticket`. Elsewhere it
carries only `brief` — the two `.enabled` files are independent, and toggling
inside a project never writes to your global one.

The lookup walks up from the working directory, so it applies in every
subdirectory of the repo:

```console
$ cd ~/projects/backend/internal/api && snip status
snip: brief,ticket
```

### When both scopes use the same name

They stay separate snippets, both listed and both enablable. A bare name means
the project one; `-g` reaches the global one.

```console
$ snip                        # inside the project
  [x] brief   (global)    Skip the preamble. Answer in under five sentences…
  [ ] brief   (project)   Keep answers to one paragraph; this repo's reviews…

$ snip show brief             # the project's
Keep answers to one paragraph; this repo's reviews are async.

$ snip -g show brief          # the global one
Skip the preamble. Answer in under five sentences unless I ask for depth.

$ snip brief                  # toggles the project one
$ snip -g brief               # toggles the global one
```

The status line disambiguates only when it has to: `snip: brief(g),brief(p)`.

Check which scopes are in play at any time:

```console
$ snip scope
global:  /home/you/.claude/snippets
project: /home/you/projects/backend/.claude/snippets
         rooted at /home/you/projects/backend
```

## A `/snip` slash command

Drop this in `~/.claude/commands/snip.md` for an arrow-key picker inside Claude
Code, where `snip pick` cannot run:

```markdown
---
description: Toggle prompt snippets appended to every prompt
argument-hint: [blank opens the picker | <name> to toggle | on/off/only/clear/show]
allowed-tools: Bash(snip:*)
---

Snippet inventory (JSON):

!`snip menu 2>&1`

Arguments given: "$ARGUMENTS"

If arguments are non-empty, run `snip $ARGUMENTS` and print the output verbatim.
If empty, present the inventory as a multi-select, then run
`snip only <selected>` (or `snip clear` if nothing was picked).
```

## Where things live

| | Path | Override |
| --- | --- | --- |
| Global snippets | `~/.claude/snippets/*.md` | `SNIP_DIR` |
| Global enabled set | `~/.claude/snippets/.enabled` | |
| Project snippets | `<root>/.claude/snippets/*.md` | |
| Project enabled set | `<root>/.claude/snippets/.enabled` | |

A snippet is a plain Markdown file; its first line is the summary shown in
listings. `.enabled` is one name per line — hand-editable, and `#` comments are
ignored. A name in `.enabled` with no matching file is skipped rather than
treated as an error, so deleting a snippet never breaks the hook.

`$HOME` is never a project root, even though `~/.claude/snippets` has exactly
the shape of the project marker.

## Command reference

| Command | Effect |
| --- | --- |
| `snip` | List both scopes |
| `snip <name>...` | Toggle |
| `snip on\|off <name>...` | Enable / disable explicitly |
| `snip only <name>...` | Set the active scope's enabled list exactly |
| `snip clear` | Disable everything in the active scope |
| `snip pick` | Interactive checkbox list (needs a TTY) |
| `snip show [name...]` | Print snippets, or all enabled text |
| `snip add <name> [text]` | Create |
| `snip edit <name>` | Open in `$EDITOR` |
| `snip rm <name>...` | Delete |
| `snip scope [show\|init]` | Report scopes, or opt this directory in |
| `snip hook` | Emit `UserPromptSubmit` JSON |
| `snip status` | One-line summary for the status line |
| `snip menu` | JSON inventory |

`-g` / `--global` makes any command act on the global scope from inside a
project. Aliases: `ls`, `cat`, `view`, `new`, `create`, `remove`, `delete`.

A snippet named after a subcommand — `on`, `show`, `list` — cannot be toggled
by bare name, since the argument parser claims it first. Use the explicit form:
`snip toggle on`.

## Development

```sh
go test ./...                                              # 24 tests
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
```

See [AGENTS.md](AGENTS.md) for architecture and conventions.
