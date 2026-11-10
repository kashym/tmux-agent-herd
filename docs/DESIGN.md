# tmux-agent-herd: Design & Implementation Plan

> This is the original design plan. For current behaviour and usage see the [README](../README.md); notably, agent integration is now harness-neutral (`herd setup <harness>`), not Claude-specific.

Nov 10, 2026 · @kashyman

## Overview

tmux-agent-herd is a TPM-installable tmux plugin that adds herdr-style agent awareness to plain tmux: per-pane agent state (working, blocked, done, idle), an at-a-glance view, alerts, and a CLI agents can use to orchestrate panes. tmux keeps doing what it already does well: sessions, panes, detach/reattach and SSH.

**In scope (v1)**

- Agent state per pane from three sources: agent hooks, OSC 7501 reports, and screen heuristics
- Status line and pane-border indicators, a popup picker, desktop notifications
- `herd` CLI for agent-driven orchestration, plus an agent skill file
- One-line install through TPM; works on Linux and macOS, tmux 3.3+

**Non-goals (v1)**

- A mouse-native, always-on sidebar like herdr's (an optional sidebar pane is phase 5)
- Patching tmux's C source; everything runs on stock tmux
- Token usage, cost tracking or approving agent actions from the UI

**How to use this plan with Claude Code:** work one phase at a time from the Implementation phases section. Start each session with the kickoff prompt at the end, point it at the phase, and require the phase's acceptance checks to pass before moving on.

## Architecture

One small Go daemon (`herdd`) per tmux server owns all agent state; everything else writes to it or reads from it. tmux pane options (`@herd_state`, `@herd_agent`, `@herd_msg`) are the shared display state, so status formats need no extra process.

&#91;embedded content: tmux-agent-herd architecture · 3 sources, 1 daemon, 3 outputs\]

Sources on the left are numbered by priority; the socket API is two-way, so agents both read state and drive panes through it.

| Component | Language | Job |
| --- | --- | --- |
| `agent-herd.tmux` | Bash | TPM entry: reads `@herd-*` options, sets keybindings and formats, starts `herdd` once per server |
| `herdd` | Go, single static binary | Holds the state table, merges sources, writes pane options, fires notifications, serves the socket API |
| Hook adapters | Bash (`herd-hook`) | Called by agent hooks (Claude Code, Codex and others); send a state event to `herdd` |
| OSC tap | Go, inside `herdd` | Per agent pane, reads the `pipe-pane -O` byte stream, parses OSC 7501, answers the probe through `pipe-pane -I` (experimental) |
| Screen poller | Go, inside `herdd` | Every 1 s, `capture-pane` on agent panes, matches per-agent pattern files |
| `herd` CLI | Go (same binary, subcommand) | Human and agent commands: list, spawn, send, read, wait, focus |
| UI | tmux formats + Bash + fzf | Status segment, pane-border dot, popup picker, optional sidebar pane |

**Why Go for the daemon:** a single static binary is easy to ship through TPM (download a release asset per OS/arch on install, fall back to `go build`). Bash alone cannot hold a long-lived socket server and stream parser reliably.

**Socket:** `herdd` listens on `$TMUX_TMPDIR/herd-<tmux socket name>.sock` with newline-delimited JSON, one request per line. One daemon per tmux server, started by the plugin and restarted by a `client-attached` hook if it is not running.

## State model and detection

Each pane has one record: `{pane_id, agent, state, kind, msg, source, updated_at}`. States are `idle`, `working`, `blocked` (kind: `permission`, `question`, `auth`), `done`, `error`, plus `none` for non-agent panes. The source that wins is the most trustworthy one that reported recently.

| Priority | Source | How it works | Freshness window |
| --- | --- | --- | --- |
| 1 | Agent hooks | Claude Code hooks (`UserPromptSubmit`, `PreToolUse`, `Notification`, `Stop`, `SessionEnd`) call `herd-hook <state>`; the script reads `$TMUX_PANE` and sends the event to `herdd` | Until the next hook event |
| 2 | OSC 7501 | `herdd` attaches `pipe-pane -IO` to agent panes, parses `ESC ] 7501 ; key=value… ST` from the output stream, and answers the ` ?  ` probe by writing the reply into the pane input | Until the next report |
| 3 | Screen heuristics | Per-agent TOML pattern file (e.g. `patterns/claude.toml`): regexes over the last 40 captured lines for blocked prompts, spinners and an idle input box | Re-evaluated every poll |
| 4 | Process only | `#{pane_current_command}` matches a known agent but nothing else reported | Shows `idle` |

**Rules**

- Agent detection: a pane is an agent pane when its foreground command matches `@herd-agents` (default: `claude codex aider gemini opencode copilot`) or a hook or OSC report names one.
- A higher-priority source silences lower ones for that pane until it goes stale (no event for `@herd-stale-after`, default 10 min) or the agent process exits.
- `done` decays to `idle` once the pane is focused (`pane-focus-in` hook), so the inbox only shows unseen results.
- Every transition is appended to `~/.local/state/herd/events.jsonl` for debugging and the `herd log` command.

**OSC 7501 is experimental.** Claude Code only turns it on if the probe is answered at startup, so `pipe-pane` must be attached before the agent starts. The plugin does this through an `after-split-window` / `after-new-window` hook and a `herd run <agent>` wrapper. If the probe reply arrives too late, the pane falls back to hooks and heuristics with no user-visible error.

## UI

All UI reads pane options that `herdd` writes, so it costs no extra process per redraw.

- **Status segment:** `#{E:@herd_status}` interpolated into the user's `status-right`, e.g. `● 2 blocked  ◐ 3 working  ✓ 1 done`. `herdd` recomputes `@herd_status` on every transition and runs `refresh-client -S`.
- **Window name badge:** an optional `window-status-format` wrapper adds the worst state of any pane in the window (blocked > error > done > working > idle) as a coloured dot.
- **Pane border:** `pane-border-status top` with a format showing agent name, state and `@herd_msg` (e.g. `claude · blocked · Approve bash: rm -rf dist?`). Off by default because it costs one row per pane.
- **Picker (`prefix + a`):** `display-popup -E 'herd pick'` opens an fzf list of agent panes sorted blocked first, with a live `capture-pane` preview. Enter switches to the pane; `ctrl-x` kills it; `ctrl-r` sends a prompt.
- **Jump keys:** `prefix + B` goes to the oldest blocked pane, `prefix + D` to the oldest unseen done pane.
- **Notifications:** on a transition to `blocked` or `done` in a pane that is not focused, `herdd` runs `@herd-notify-cmd` (default: `notify-send` on Linux, `osascript` on macOS, else `display-message`). Rate-limited to one per pane per 30 s.
- **Sidebar pane (phase 5, optional):** `herd sidebar` runs a small TUI in a 30-column pane on the left of a window, refreshed from the socket. It costs screen width, so it is opt-in.

## Orchestration CLI and agent skill

`herd` gives agents (and scripts) a small, stable API over tmux, so they never have to compose raw `tmux` commands. Every command takes `--json`.

| Command | What it does | Built on |
| --- | --- | --- |
| `herd ls` | List panes with agent, state, msg, cwd | `herdd` state + `list-panes` |
| `herd spawn [--right/--down/--window] [--name N] -- <cmd>` | New pane running a command; prints its pane id | `split-window -P -F '#{pane_id}'` |
| `herd run <agent> [args]` | Like spawn, but attaches the OSC tap first, then starts the agent | `pipe-pane -IO` + `send-keys` |
| `herd send <pane> <text> [--enter]` | Type into a pane; refuses if the pane is `working` unless `--force` | `send-keys -l` |
| `herd read <pane> [--lines N]` | Last N lines of the pane, ANSI stripped | `capture-pane -p -J` |
| `herd wait <pane> --for done/idle/blocked [--timeout S]` | Block until that state | Subscription on `herdd` socket |
| `herd focus <pane>` / `herd close <pane>` | Switch to or kill a pane | `select-pane`, `kill-pane` |
| `herd log [pane]` | Recent state transitions | `events.jsonl` |

**Safety:** `send` and `close` only act on panes in the caller's own tmux session unless `--any-session` is passed, and `close` never touches the caller's own pane.

**Agent skill:** ship `skills/herd/SKILL.md`, a short guide an agent loads to learn the commands, with a worked example (spawn two worker agents, `wait` for both, `read` results). `herd install-skill` copies it into `~/.claude/skills/herd/`.

## Configuration and packaging

Users install with two lines in `~/.tmux.conf`, then `prefix + I`:

```
set -g @plugin 'tmux-plugins/tpm'
set -g @plugin '<you>/tmux-agent-herd'
```

| Option | Default | Effect |
| --- | --- | --- |
| `@herd-agents` | `claude codex aider gemini opencode copilot` | Commands treated as agents |
| `@herd-status` | `on` | Append the status segment to `status-right` |
| `@herd-border` | `off` | Show agent state in pane borders |
| `@herd-picker-key` | `a` | Key for the popup picker |
| `@herd-notify` | `blocked,done` | States that trigger a notification |
| `@herd-notify-cmd` | auto | Custom notification command |
| `@herd-osc` | `on` | Attach the OSC 7501 tap to agent panes |
| `@herd-poll-ms` | `1000` | Screen poll interval |
| `@herd-stale-after` | `600` | Seconds before a hook/OSC state is treated as stale |

**Repo layout**

```
tmux-agent-herd/
  agent-herd.tmux          # TPM entry point (executable)
  scripts/
    install-binary.sh      # fetch release asset for OS/arch, else go build
    herd-hook              # hook adapter called by agents
    claude-hooks.json      # snippet merged into ~/.claude/settings.json
  cmd/herd/                # Go: daemon + CLI in one binary
  internal/{state,osc,poll,tmux,notify,api}/
  patterns/{claude,codex,aider,gemini}.toml
  skills/herd/SKILL.md
  test/                    # Go unit tests + bats integration tests
  .goreleaser.yaml         # linux/darwin × amd64/arm64
  README.md  LICENSE (MIT)
```

**Agent hook setup:** `herd setup claude` merges the hook entries into `~/.claude/settings.json` after showing a diff and asking for confirmation. It never rewrites the file without approval.

**License:** MIT. Detection patterns are written from scratch, not copied from herdr (one herdr fork is AGPL-3.0).

## Implementation phases

Six phases, each shippable on its own. Phases 0–2 give a useful plugin; 3–5 add the herdr-level extras.

1. **Phase 0 — Skeleton and daemon (1–2 sessions)**
   - Go module with `herd` binary: `daemon`, `ls`, `version` subcommands; socket server with JSON lines
   - `internal/tmux`: thin wrapper over `tmux` commands with the right `-S` socket; tests against a throwaway server (`tmux -L herdtest`)
   - `agent-herd.tmux` that installs or builds the binary and starts one daemon per server
   - *Done when:* installing through TPM on a clean machine starts `herdd`, and `herd ls` lists every pane
2. **Phase 1 — Hook source and status line**
   - `herd-hook` adapter, `herd setup claude` with diff and confirm
   - State table, priority merge, `@herd_state` / `@herd_status` writes, `refresh-client -S`
   - Status segment and window badge
   - *Done when:* in a live Claude Code session, the status line shows working while it runs, blocked on a permission prompt and done when it stops, within 1 s
3. **Phase 2 — Picker, jump keys, notifications**
   - `herd pick` (fzf + preview) in `display-popup`; `prefix + B` / `prefix + D`
   - Notifier with rate limit; `done` decays to `idle` on focus
   - *Done when:* with 4 agents in 3 windows, one key takes you to the blocked one and a desktop notification fired for it
4. **Phase 3 — Screen heuristics**
   - Pattern files for claude, codex, aider, gemini; poller only on agent panes
   - Recorded fixture captures in `test/fixtures/` with expected states
   - *Done when:* agents with no hooks set up still show correct states on all fixtures, and CPU stays under 1% with 10 agent panes
5. **Phase 4 — Orchestration CLI and skill**
   - `spawn`, `run`, `send`, `read`, `wait`, `focus`, `close`, `log`, all with `--json`
   - `skills/herd/SKILL.md` and `herd install-skill`
   - *Done when:* a Claude Code session given only the skill spawns two worker agents, waits for both and summarises their output
6. **Phase 5 — OSC 7501 tap and sidebar (experimental)**
   - `pipe-pane -IO` tap, OSC parser (fuzz-tested), probe reply; `herd run` attaches the tap before launch
   - Optional `herd sidebar` TUI
   - Release: goreleaser, README with GIF, submit to the TPM plugin list
   - *Done when:* Claude Code launched via `herd run claude` reports state over OSC 7501 (verified in `herd log` with `source=osc`), and turning the tap off falls back cleanly

## Testing, risks and kickoff

**Testing**

- Go unit tests for the state merge, OSC parser (plus `go test -fuzz`) and pattern matching against fixtures
- bats integration tests that start an isolated server (`tmux -L herdtest -f /dev/null`), run a fake agent script that prints known output, and assert pane options
- CI matrix: Ubuntu and macOS, tmux 3.3a and latest

**Risks**

| Risk | Mitigation |
| --- | --- |
| Only one `pipe-pane` per pane, so the OSC tap clashes with tmux-logging and similar plugins | Detect an existing pipe, skip the tap for that pane and log it; document the conflict |
| OSC probe reply arrives too late, so Claude Code never enables the protocol | Tap attached by `herd run` before launch; hooks remain the primary source |
| Agent UIs change and break screen patterns | Patterns in data files, versioned fixtures, hooks preferred over heuristics |
| Status redraws feel slow or cost CPU | Event-driven writes from `herdd`, polling only agent panes, no shell spawned per redraw |
| Editing users' `~/.claude/settings.json` | Diff and confirm, keep a backup, never run silently |

**Open questions**

- Go for the daemon, or Rust to match herdr's single-binary story?
- Should `blocked` permission prompts be answerable from the picker (`y`/`n` sent to the pane), or stay out of scope?

**Kickoff prompt for Claude Code**

```
You are building tmux-agent-herd, a TPM plugin that adds agent awareness to tmux.
The design doc is in docs/DESIGN.md (paste this doc there). Work on Phase <N> only.
Rules:
- Read DESIGN.md first; follow its components, names and options exactly.
- Write tests first for each component; run them against an isolated server
  (tmux -L herdtest -f /dev/null), never the user's real tmux server.
- Do not edit ~/.tmux.conf or ~/.claude/settings.json; use temp HOME in tests.
- Finish by running the full test suite and checking every 'Done when' item for
  the phase; report anything that did not pass.
```
