# Changelog

## 0.2.0

Herd mode, and a much better tmux citizen.

- **Herd mode** (`prefix + a`): a dashboard docked on the left as a popup.
  Every session and agent, most urgent first. Enter jumps there, `/` filters,
  `s` sends a prompt, `x` kills, `p` pins a sidebar to every window.
- **Sidebars**: `herd mode --pin on` or `@herd-sidebar on` keeps a live
  sidebar pane in every window; `herd sidebar --toggle` for one window.
- **Never clobbers bindings**: herd binds one key by default and skips any key
  that is already bound, telling you once. Bindings left on `B`/`D`/`S` by
  0.1.0 are removed and `prefix + D` gets tmux's `choose-client` back.
  The direct jump/sidebar keys are now opt-in.
- **Status placeholders**: `#{herd}` and `#{herd_badge}` in your own formats.
- **`herd doctor`**: everything herd changed, key conflicts, daemon and agent
  CLI status.
- **`herd setup --print`** for dotfiles/Nix-managed configs; `--remove` to
  take herd out of an agent CLI; `install-skill --remove`.
- `@herd-path off` to keep PATH untouched; installed skills use herd's
  absolute path (avoids clashes with Laravel Herd's `herd`).
- Release downloads are checksum-verified.
- One-time welcome message; `scripts/demo.sh` for a throwaway demo.

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
