# snip

Toggle reusable instruction snippets that get appended to every Claude Code
prompt, via a `UserPromptSubmit` hook. One stdlib-only binary, no dependencies.

## Install

```sh
go build -o ~/.local/bin/snip .
```

Snippets live in `~/.claude/snippets/*.md` (override with `SNIP_DIR`); the
enabled set is `~/.claude/snippets/.enabled`, one name per line.

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
