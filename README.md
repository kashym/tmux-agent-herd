# tmux-agent-herd

[![CI](https://github.com/kashym/tmux-agent-herd/actions/workflows/ci.yml/badge.svg)](https://github.com/kashym/tmux-agent-herd/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/kashym/tmux-agent-herd)](https://github.com/kashym/tmux-agent-herd/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Know which of your coding agents needs you, without leaving tmux.**

You run Claude Code in one pane, Codex in another, pi in a third. One is
waiting for permission, one finished ten minutes ago, one is still working, and
you only find out by flipping through panes. tmux-agent-herd puts every
agent's state in your status line, sends a desktop notification when one needs
you, and takes you to it with one key.

```
status line   ● 1 blocked  ◐ 2 working  ✓ 1 done
window list   ● 1:api   ◐ 2:web   ✓ 3:docs   4:notes
pane border   codex · blocked · Bash: rm -rf dist
```

Works with **Claude Code, Codex CLI, Gemini CLI, GitHub Copilot CLI, OpenCode,
pi and aider**, and anything else that can run a script. Plain tmux, Linux and
macOS, no tmux patches.

<!-- TODO: add demo.gif here (see "Recording a demo" in CONTRIBUTING.md) -->

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/kashym/tmux-agent-herd/main/install.sh | bash
```

That one command:

1. checks you have tmux 3.3+ and git,
2. downloads the plugin to `~/.tmux/plugins/tmux-agent-herd` and its `herd` binary,
3. adds one line to your tmux config (a TPM `@plugin` line if you use TPM) and loads it into running tmux,
4. finds the agent CLIs you have installed and connects each one, **showing you the exact change first** and backing up every file it edits,
5. optionally installs a skill so your agents can use herd themselves.

Nothing changes without a yes. Prefer to read the script first? Download it,
read it, and run `bash install.sh` (`--help` for options, `--yes` for no
questions).

<details>
<summary>Install with TPM instead</summary>

```tmux
set -g @plugin 'tmux-plugins/tpm'
set -g @plugin 'kashym/tmux-agent-herd'
```

Press `prefix + I`, then connect your agents from any tmux pane:

```sh
herd setup all       # shows each change as a diff and asks first
herd install-skill   # optional
```
</details>

**Requirements:** tmux 3.3 or newer, git, Linux or macOS. `fzf` is optional
(nicer picker). Prebuilt binaries are downloaded automatically; Go is only
needed if no release exists for your platform.

## Use it

| Key | What it does |
| --- | --- |
| `prefix + a` | Picker: all agents, the ones that need you first, with a live preview. `enter` go there, `ctrl-r` send a prompt, `ctrl-x` kill |
| `prefix + B` | Jump to the agent that has been **blocked** longest |
| `prefix + D` | Jump to the agent that **finished** and you haven't looked at yet |

| State | Meaning |
| --- | --- |
| `●` **blocked** | waiting for you: a permission prompt, a question, or a login |
| `◐` **working** | busy |
| `✓` **done** | finished its turn and you haven't looked yet. Turns into idle once you focus the pane |
| `✗` **error** | the last turn failed (e.g. the provider returned an error) |
| `○` **idle** | waiting for a new prompt |

You get a desktop notification when an agent you're not looking at becomes
blocked or done (at most one per pane every 30 s).

Panes running an agent are detected automatically. Connecting the agent's own
hooks (step 4 of the installer, or `herd setup`) makes the state exact and
instant. Without hooks herd falls back to reading the screen.

## Supported agent CLIs

| Agent CLI | `herd setup <name>` installs | Reports |
| --- | --- | --- |
| Claude Code (`claude`) | hooks in `~/.claude/settings.json` | working, blocked, done |
| Codex CLI (`codex`) | `~/.codex/hooks.json`, then **trust them once with `/hooks` in Codex** | working, blocked, done |
| Gemini CLI (`gemini`) | hooks in `~/.gemini/settings.json` | working, blocked, done |
| GitHub Copilot CLI (`copilot`) | `~/.copilot/hooks/herd.json` | working, blocked, done, error |
| OpenCode (`opencode`) | plugin `~/.config/opencode/plugins/herd.js` | working, blocked, done, error |
| pi (`pi`) | extension `~/.pi/agent/extensions/herd.ts` | working, blocked, done, error |
| aider (`aider`) | `notifications-command` in `~/.aider.conf.yml` | done (the rest from the screen) |

```sh
herd setup --list            # what's installed and what's connected
herd setup codex pi          # connect specific ones
herd setup --remove all      # take herd back out
```

**Your agent isn't listed?** Call the adapter from its hooks:

```sh
~/.tmux/plugins/tmux-agent-herd/scripts/herd-hook --agent mytool working
~/.tmux/plugins/tmux-agent-herd/scripts/herd-hook --agent mytool --kind permission --msg "Approve rm?" blocked
~/.tmux/plugins/tmux-agent-herd/scripts/herd-hook --agent mytool done     # also: idle, error, none
```

It never prints and always exits 0, so it can't break your agent. Add the
command name to `@herd-agents` so the pane is recognized before the first
event, and [open an issue](https://github.com/kashym/tmux-agent-herd/issues/new?template=new_harness.yml)
so it can be supported out of the box.

## Let your agents run other agents

`herd` is also a small, safe CLI that agents (and scripts) use to start
workers in new panes, hand them tasks, and wait for results:

```sh
herd run --name tests --right codex          # start an agent in a new pane
herd send tests "fix the failing test" --enter
herd wait tests --for done,blocked --timeout 1800
herd read tests --lines 60
herd ls --json
```

`herd install-skill` teaches this to every installed agent CLI that supports
[Agent Skills](https://agentskills.io), so you can just ask your agent to "run
these three tasks in parallel workers". Safety rails: agents can only type into
and close panes in their own tmux session, never close their own pane, and
can't type into a working agent without `--force`.

Run `herd help` for every command.

## Configure

Set these in your tmux config before the plugin line.

| Option | Default | Effect |
| --- | --- | --- |
| `@herd-agents` | `claude codex gemini copilot opencode pi aider` | Commands treated as agents |
| `@herd-status` | `on` | Add the summary to `status-right` |
| `@herd-window-badge` | `on` | Dot with the worst state in front of each window name |
| `@herd-border` | `off` | Agent, state and message in pane borders |
| `@herd-picker-key` | `a` | Picker key (`off` to disable) |
| `@herd-blocked-key` | `B` | Jump-to-blocked key |
| `@herd-done-key` | `D` | Jump-to-done key (replaces tmux's default `choose-client` on `D`) |
| `@herd-notify` | `blocked,done` | States that notify (`off` to disable) |
| `@herd-notify-cmd` | auto | Your own notifier, run as `sh -c "$cmd" herd <title> <body>` |
| `@herd-poll-ms` | `1000` | Screen check interval |
| `@herd-stale-after` | `600` | Seconds before a hook-reported state is distrusted |
| `@herd-patterns-dir` | | Extra screen-pattern directory |

Want the summary somewhere else? Set `@herd-status off` and put
`#{E:@herd_status}` where you like. Each pane also has `@herd_state`,
`@herd_agent` and `@herd_msg` options for your own formats.

## Uninstall

```sh
bash ~/.tmux/plugins/tmux-agent-herd/install.sh --uninstall
```

This removes herd from your agent CLIs' configs (with diffs and backups), the
skill, the tmux config line and the plugin.

## Troubleshooting

- **Nothing in the status line.** A theme or plugin loaded after herd probably
  overwrote `status-right`. Load herd last, or add `#{E:@herd_status}` to your
  theme's `status-right` yourself.
- **An agent pane isn't detected.** Check `herd ls` for its `COMMAND`, and add
  that name to `@herd-agents`.
- **States are wrong or late.** Run `herd setup --list` and connect that agent
  CLI. For Codex, remember to trust the hooks with `/hooks`. Restart running
  agent sessions after setup.
- **`herd: command not found`.** Panes opened before the install don't have it
  on PATH. Open a new pane, or use `~/.tmux/plugins/tmux-agent-herd/bin/herd`.
- **Still stuck?** `herd log` shows every state change and where it came from,
  and `~/.local/state/herd/herdd.log` has the daemon log. Please include both in
  [a bug report](https://github.com/kashym/tmux-agent-herd/issues/new?template=bug_report.yml).

## How it works

One small daemon per tmux server (`herdd`, part of the `herd` binary) keeps
each pane's state, chosen from the most trustworthy source that reported
recently:

1. **Agent hooks and extensions** (`herd setup`), which report instantly.
2. **Screen patterns**, a per-agent regex file in `patterns/` (claude, codex,
   gemini, aider) checked once a second, only on agent panes and only when the
   screen changed.
3. **The process name**, which marks the pane as an idle agent.

It writes the result into tmux pane options, which the status line reads, so
nothing runs per redraw. It uses under 1% CPU with 10 agents. Every
change is logged to `~/.local/state/herd/events.jsonl` (`herd log`). The
[original design doc](docs/DESIGN.md) has the background.

## Contributing

Issues and PRs are very welcome, especially **support for more agent CLIs**
and **real screen captures** that improve detection. See
[CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
