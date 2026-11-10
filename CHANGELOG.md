# Changelog

## 0.2.0

- **Sidebar**: a live dashboard pane with sessions and agents sorted by
  urgency; keyboard and mouse navigation, send a prompt, kill a pane.
  Toggle with `prefix + S`, or `set -g @herd-sidebar on` for every window.
- `herd setup --remove` and `install-skill --remove`; the installer's
  `--uninstall` uses them

## 0.1.0

First release.

- Agent state per tmux pane (working, blocked, done, idle, error) from agent
  hooks/extensions, screen heuristics and process detection
- Status line segment, window badges, optional pane borders
- Popup picker (`prefix + a`), jump to blocked (`prefix + B`) and done (`prefix + D`)
- Desktop notifications for blocked/done agents you aren't looking at
- `herd setup` for Claude Code, Codex, Gemini CLI, Copilot CLI, OpenCode, pi and aider
- `herd` CLI for orchestration: spawn, run, send, read, wait, focus, close, log
- Agent skill (`herd install-skill`) so agents can coordinate other agents
- One-line installer with uninstall
