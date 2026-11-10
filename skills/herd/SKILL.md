---
name: herd
description: Orchestrate other coding agents running in tmux panes with the `herd` CLI — spawn worker agents, send them prompts, wait until they finish or get blocked, and read their output. Use when asked to run tasks in parallel across agents, delegate to sub-agents in tmux, or check on other agent panes.
---

# herd: drive agent panes in tmux

`herd` (from the tmux-agent-herd plugin) gives you a small, stable API over
tmux. Use it instead of composing raw `tmux` commands. Every pane has an agent
state: `idle`, `working`, `blocked` (needs a human: permission, question or
auth), `done` (finished a turn, not yet looked at), `error`, or `none`
(not an agent).

<!-- herd-bin -->

You must be running inside tmux (`$TMUX_PANE` is set). `send` and `close` only
act on panes in your own tmux session unless you pass `--any-session`, and
`close` never closes your own pane.

## Commands

| Command | Use |
| --- | --- |
| `herd ls [--agents] [--json]` | List panes with agent, state, message and cwd |
| `herd run [--right\|--down\|--window] [--name N] [--cwd DIR] <agent> [args]` | Start an agent in a new pane; prints the pane id |
| `herd spawn [--right\|--down\|--window] [--name N] [--keep] -- <cmd...>` | Run any command in a new pane (`--keep` keeps it after exit) |
| `herd send <pane> <text> --enter` | Type a prompt into a pane and submit it. Refuses if the pane is `working` (use `--force` only if you mean to interrupt) |
| `herd wait <pane> --for done,idle,blocked [--timeout S]` | Block until the pane reaches one of the states. Exit 0 = reached, 2 = timed out |
| `herd read <pane> [--lines N] [--json]` | Last N lines of the pane's screen, plain text |
| `herd focus <pane>` / `herd close <pane>` | Show a pane to the human / kill it |
| `herd log [pane]` | Recent state transitions |

`<pane>` is a pane id (`%7`), a tmux target (`main:2.1`), or the `--name`
you gave it. Prefer names: they make your commands readable.

Every command accepts `--json` where output matters; parse that rather than
the tables.

## Rules of thumb

- After `send`, `wait` ignores states from before your input, so
  `send … --enter` followed by `wait --for done` waits for *this* task.
- Always include `blocked` in `--for` and always set `--timeout`, so you never
  wait forever on an agent that is asking a human for permission. When a worker
  is blocked, `read` it and tell the user which pane needs them
  (`herd focus <pane>` brings it up).
- `read` shows the screen, not a transcript; ask workers to end with a short,
  clearly marked summary (e.g. a line starting with `RESULT:`), then read
  enough lines to capture it.
- Give each worker a distinct `--cwd` (or git worktree) if they edit files.
- Close the panes you spawned when you are done, unless the user wants them.

## Worked example: two workers in parallel

```bash
# 1. Start two workers next to this pane. Use whichever agent CLI the user
#    works with: claude, codex, gemini, copilot, opencode, pi, aider, ...
#    (check `herd ls --agents` to see what is already running).
herd run --name tests --right codex
herd run --name docs --down codex
herd wait tests --for idle --timeout 60   # agent UI is up
herd wait docs  --for idle --timeout 60

# 2. Give each a task.
herd send tests "Run the test suite, fix any failing test, then print one line starting with RESULT: summarising what you changed." --enter
herd send docs  "Update README.md for the new --json flags, then print one line starting with RESULT: summarising the change." --enter

# 3. Wait for both (blocked counts as needing attention).
herd wait tests --for done,idle,blocked --timeout 1800 --json
herd wait docs  --for done,idle,blocked --timeout 1800 --json

# 4. Collect results.
herd read tests --lines 60 | grep RESULT:
herd read docs  --lines 60 | grep RESULT:

# 5. Clean up.
herd close tests
herd close docs
```

If a `wait` returns `blocked`, do not approve anything yourself: report the
pane and its message (from `herd ls --json`) to the user.
