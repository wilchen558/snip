# snip

Reusable instruction snippets, appended to every Claude Code prompt.

Write an instruction once, then switch it on and off instead of retyping it.
A `UserPromptSubmit` hook appends whatever is enabled to each prompt you send.

```console
$ snip
  [ ] ask-questions    Never assume. If any part of this request i…
  [x] brief            Skip the preamble. Answer in under five sen…
  [ ] no-new-deps      Do not add new dependencies. Solve it with …

$ snip ask-questions
  [x] ask-questions    Never assume. If any part of this request i…
  [x] brief            Skip the preamble. Answer in under five sen…
  [ ] no-new-deps      Do not add new dependencies. Solve it with …
```

One binary. [kong](https://github.com/alecthomas/kong) is the only dependency.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/wilchen558/snip/main/install.sh | sh
```

Downloads the latest release for your platform, verifies its checksum, and puts
`snip` in `~/.local/bin` (override with `SNIP_BIN_DIR`, pin with `SNIP_VERSION`).

With Go installed:

```sh
go install github.com/wilchen558/snip@latest
```

From source:

```sh
git clone https://github.com/wilchen558/snip && cd snip
go build -ldflags "-X main.version=$(git describe --tags --always)" -o ~/.local/bin/snip .
```

`~/.local/bin` must be on your `PATH`.

## Wire it into Claude Code

```json
// ~/.claude/settings.json
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

- `snip hook` prints the enabled snippets as `additionalContext` JSON, and
  nothing at all when none are enabled.
- `snip status` puts `snip: brief,no-new-deps` in the status line.
- `respondToBashCommands: false` is what makes the next section free.

Restart Claude Code for the status line; the hook works immediately.

## Toggling from inside Claude Code

Type `!` in the input box to run a command. With `respondToBashCommands: false`
there is no assistant turn, so toggling costs nothing:

```console
> ! snip brief
  [ ] ask-questions    Never assume. If any part of this request i…
  [x] brief            Skip the preamble. Answer in under five sen…
  [ ] no-new-deps      Do not add new dependencies. Solve it with …
```

Your next prompt carries `brief`. The status line shows what is active, so you
never have to ask.

`snip pick` is the exception — it draws a full-screen widget and Claude Code's
`!` box has no TTY. Use it in a terminal, or see [`/snip`](#a-snip-slash-command).

## Commands

```sh
snip                        # list; [x] means enabled
snip brief                  # toggle one
snip brief no-new-deps      # toggle several
snip on brief               # enable regardless of current state
snip off brief              # disable
snip only brief             # make this the exact enabled set
snip clear                  # disable everything

snip add hurry "Answer in under five sentences."
snip add hurry              # prompts for the body, ctrl-d to finish
echo "Cite file:line." | snip add cite
snip edit hurry             # opens $EDITOR
snip rm hurry
```

```console
$ snip show brief
Skip the preamble. Answer in under five sentences unless I ask for depth.

$ snip show                 # everything enabled, as the prompt receives it
Skip the preamble. Answer in under five sentences unless I ask for depth.

Do not add new dependencies. Solve it with what's already in the project, or
tell me which dependency you'd need and why before adding it.
```

```console
$ snip pick
  space toggle · a all · n none · enter save · q cancel
> [x] brief            Skip the preamble. Answer in under five sentences…
  [ ] no-new-deps      Do not add new dependencies. Solve it with what's…
  [x] test-first       Write the failing test before the implementation…
```

## Per-project snippets

A repository can carry its own snippets alongside your global ones. Both apply.

```console
$ cd ~/projects/backend
$ snip scope init
created /home/you/projects/backend/.claude/snippets

$ snip add ticket "Reference the Jira ticket ID in every commit message."
created /home/you/projects/backend/.claude/snippets/ticket.md

$ snip ticket
  [x] brief        (global)   Skip the preamble. Answer in under five sen…
  [ ] no-new-deps  (global)   Do not add new dependencies. Solve it with …
  [x] ticket       (project)  Reference the Jira ticket ID in every commi…
  project: /home/you/projects/backend

$ cd internal/api && snip status      # applies repo-wide
snip: brief,ticket
```

Prompts from that repo carry `brief` **and** `ticket`; elsewhere, only `brief`.
The two `.enabled` files are independent — toggling in a project never writes to
your global one.

### Same name in both scopes

Two separate snippets. A bare name means the project one, `-g` the global one,
and both can be enabled at once.

```console
$ snip
  [x] brief   (global)    Skip the preamble. Answer in under five sentences…
  [ ] brief   (project)   Keep answers to one paragraph; reviews are async…
```

```sh
snip show brief          # the project one
snip -g show brief       # the global one
snip brief               # toggles the project one
snip -g brief            # toggles the global one
```

The status line disambiguates only when it must: `snip: brief(g),brief(p)`.

## A `/snip` slash command

For an arrow-key picker inside Claude Code, where `snip pick` cannot run. Drop
this in `~/.claude/commands/snip.md`:

````markdown
---
description: Toggle prompt snippets appended to every prompt
argument-hint: [blank opens the picker | <name> to toggle | on/off/only/clear]
allowed-tools: Bash(snip:*)
---

Snippet inventory (JSON):

!`snip menu 2>&1`

Arguments given: "$ARGUMENTS"

If arguments are non-empty, run `snip $ARGUMENTS` and print the output verbatim.
If empty, present the inventory as a multi-select, then run
`snip only <selected>` (or `snip clear` if nothing was picked).
````

Unlike `! snip`, this costs a model turn.

## Files

| | Path |
| --- | --- |
| Global snippets | `~/.claude/snippets/*.md` (override with `SNIP_DIR`) |
| Project snippets | `<root>/.claude/snippets/*.md` |
| Enabled set | `.enabled` in either directory |

A snippet is a Markdown file whose first line is the summary shown in listings.
`.enabled` is one name per line, hand-editable, `#` comments ignored. A name
with no matching file is skipped, so deleting a snippet never breaks the hook.

`$HOME` is never a project root, though `~/.claude/snippets` has exactly the
shape of the project marker.

## Reference

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
| `snip help [command]` | Usage, overall or for one command |
| `snip hook` | Emit `UserPromptSubmit` JSON |
| `snip status` | One-line summary for the status line |
| `snip menu` | JSON inventory |

`-g` acts on the global scope from inside a project.
Aliases: `ls`, `cat`, `view`, `new`, `create`, `remove`, `delete`.

A snippet named after a subcommand — `on`, `show`, `list` — can't be toggled by
bare name; the parser claims it first. Use `snip toggle on`.

## Development

```sh
go test ./...        # 26 tests
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
```

Releases are cut by tagging; GoReleaser builds linux, macOS and Windows for
amd64 and arm64, and `install.sh` reads whatever the latest release holds.

```sh
git tag -a v0.1.0 -m "v0.1.0" && git push origin v0.1.0
```

[AGENTS.md](AGENTS.md) has the architecture and the traps.

## Licence

[MIT](LICENSE).
