# snip

Toggle reusable instruction snippets that get appended to every Claude Code
prompt, via a `UserPromptSubmit` hook. One stdlib-only binary, no dependencies.

## Install

```sh
go build -o ~/.local/bin/snip .
```

## Scopes

Snippets live in two places, and both apply at once:

| Scope | Directory | Enabled set |
| --- | --- | --- |
| global | `~/.claude/snippets/` (override with `SNIP_DIR`) | `.enabled` in that directory |
| project | `<root>/.claude/snippets/` | `.enabled` in that directory |

A directory opts in to project scope with `snip scope init`; the lookup then
walks up from the working directory, so it applies in every subdirectory of the
repo. `$HOME` is never a project root.

A prompt receives every enabled global snippet, then every enabled project one.
The two `.enabled` files are independent — toggling in a project never writes to
the global manifest.

When both scopes define the same name, a bare `snip brief` means the project
one; `snip -g brief` means the global one. Both can be enabled simultaneously.

## Wire it into Claude Code

`~/.claude/settings.json`:

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

`respondToBashCommands: false` makes `! snip brief` cost no model turn.

## Use

```sh
snip                  # list, [x] = enabled
snip brief            # toggle
snip on brief hurry   # enable
snip only brief       # enable exactly these
snip clear
snip pick             # checkbox TUI (needs a real terminal, not Claude Code's ! box)
snip scope            # which scopes are active
snip scope init       # opt this directory in to project snippets
snip -g brief         # act on the global scope from inside a project
snip show brief       # print one snippet in full
snip show             # all enabled text, i.e. what reaches a prompt
snip add hurry "Answer in under five sentences."   # or: snip add (prompts)
```

`snip hook`, `snip status` and `snip menu` are the machine-facing commands used
by the hook, the status line, and the `/snip` slash command respectively.

## Layout

| File | Role |
| --- | --- |
| `main.go` | CLI dispatch and every subcommand |
| `snippets.go` | on-disk store: snippet files + enabled manifest |
| `picker.go` | raw-mode checkbox TUI (stty, no third-party terminal lib) |

## Development

```sh
go test ./...
go build -ldflags "-X main.version=$(git describe --tags --always)" -o ~/.local/bin/snip .
```

`Store` is plain file I/O against a directory, so tests point `SNIP_DIR` at a
`t.TempDir()`. Manifest mutations take an advisory lock and write atomically;
`TestConcurrentEnableKeepsEveryUpdate` guards that — without the lock it loses
roughly half of twelve concurrent toggles.
