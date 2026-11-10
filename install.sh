#!/usr/bin/env bash
# tmux-agent-herd installer.
#
#   curl -fsSL https://raw.githubusercontent.com/kashym/tmux-agent-herd/main/install.sh | bash
#
# What it does (asking before each change, backing up anything it edits):
#   1. checks tmux >= 3.3 and git
#   2. clones the plugin to ~/.tmux/plugins/tmux-agent-herd (or updates it)
#   3. installs the `herd` binary (release download, or `go build`)
#   4. adds the plugin to your tmux config (TPM line if you use TPM,
#      otherwise a run-shell line) and loads it into running tmux
#   5. connects the agent CLIs it finds (Claude Code, Codex, Gemini CLI,
#      Copilot CLI, OpenCode, pi, aider) — each one shown as a diff first
#   6. optionally installs the herd skill so your agents can use the CLI
#
# Options:
#   --yes          answer yes to every question
#   --no-agents    skip step 5
#   --no-skill     skip step 6
#   --uninstall    undo everything this script did
#   --help
#
# Environment: HERD_REPO_URL (git URL to clone), HERD_BRANCH (default main),
# HERD_PLUGIN_DIR (where to put the plugin).
set -euo pipefail

REPO_URL="${HERD_REPO_URL:-https://github.com/kashym/tmux-agent-herd.git}"
BRANCH="${HERD_BRANCH:-main}"
PLUGIN_SPEC="kashym/tmux-agent-herd"
YES=0 AGENTS=1 SKILL=1 UNINSTALL=0

for arg in "$@"; do
	case "$arg" in
	-y | --yes) YES=1 ;;
	--no-agents) AGENTS=0 ;;
	--no-skill) SKILL=0 ;;
	--uninstall) UNINSTALL=1 ;;
	-h | --help)
		cat <<'USAGE'
Usage: install.sh [--yes] [--no-agents] [--no-skill] [--uninstall]

Installs tmux-agent-herd, adds it to your tmux config, and connects the
agent CLIs it finds. Asks before every change and backs up edited files.

  --yes          answer yes to every question
  --no-agents    don't connect agent CLIs (screen detection still works)
  --no-skill     don't install the herd skill for your agents
  --uninstall    undo everything this script did
USAGE
		exit 0
		;;
	*)
		echo "unknown option: $arg (see --help)" >&2
		exit 2
		;;
	esac
done

# --- output helpers -------------------------------------------------------
if [ -t 1 ]; then
	B=$'\e[1m' G=$'\e[32m' Y=$'\e[33m' R=$'\e[31m' D=$'\e[2m' N=$'\e[0m'
else
	B="" G="" Y="" R="" D="" N=""
fi
step() { printf '\n%s==>%s %s%s%s\n' "$G" "$N" "$B" "$*" "$N"; }
info() { printf '    %s\n' "$*"; }
warn() { printf '    %s!%s %s\n' "$Y" "$N" "$*"; }
die() {
	printf '%serror:%s %s\n' "$R" "$N" "$*" >&2
	exit 1
}

# ask "question" default(y|n) -> 0 for yes. Reads the terminal even when
# this script is piped from curl.
ask() {
	local q="$1" def="${2:-y}" hint ans
	[ "$YES" = 1 ] && return 0
	if ! { exec 3</dev/tty; } 2>/dev/null; then
		[ "$def" = y ] # no terminal: take the default
		return
	fi
	hint="[Y/n]"
	[ "$def" = n ] && hint="[y/N]"
	printf '    %s %s ' "$q" "$hint" >/dev/tty
	read -r ans <&3 || ans=""
	exec 3<&-
	ans="$(printf '%s' "$ans" | tr '[:upper:]' '[:lower:]')"
	[ -z "$ans" ] && ans="$def"
	[ "$ans" = y ] || [ "$ans" = yes ]
}

backup() {
	if [ -f "$1" ]; then
		local b
		b="$1.herd-backup-$(date +%Y%m%d-%H%M%S)"
		cp -p "$1" "$b"
		info "${D}backup: $b${N}"
	fi
}

# --- locations ------------------------------------------------------------
tmux_conf() {
	local xdg="${XDG_CONFIG_HOME:-$HOME/.config}/tmux/tmux.conf"
	if [ -f "$HOME/.tmux.conf" ]; then
		echo "$HOME/.tmux.conf"
	elif [ -f "$xdg" ]; then
		echo "$xdg"
	else
		echo "$HOME/.tmux.conf"
	fi
}
CONF="$(tmux_conf)"
uses_tpm() { [ -f "$CONF" ] && grep -Eq "^[[:space:]]*set(-option)?[[:space:]]+-g[[:space:]]+@plugin[[:space:]]+['\"]tmux-plugins/tpm['\"]" "$CONF"; }
PLUGINS_DIR="${TMUX_PLUGIN_MANAGER_PATH:-$HOME/.tmux/plugins}"
DIR="${HERD_PLUGIN_DIR:-${PLUGINS_DIR%/}/tmux-agent-herd}"
HERD="$DIR/bin/herd"
tmux_running() { tmux list-sessions >/dev/null 2>&1; }

# --- uninstall ------------------------------------------------------------
if [ "$UNINSTALL" = 1 ]; then
	step "Uninstalling tmux-agent-herd"
	if [ -x "$HERD" ]; then
		if ask "Remove herd from your agent CLIs' configs (shown as diffs)?" y; then
			if [ "$YES" = 1 ]; then "$HERD" setup --remove all --yes || true; else "$HERD" setup --remove all || true; fi
		fi
		"$HERD" install-skill --remove >/dev/null 2>&1 || true
		if tmux_running; then "$HERD" daemon --stop >/dev/null 2>&1 || true; fi
	fi
	if [ -f "$CONF" ] && grep -q "tmux-agent-herd" "$CONF"; then
		if ask "Remove the tmux-agent-herd lines from $CONF?" y; then
			backup "$CONF"
			tmp="$(mktemp)"
			grep -v "tmux-agent-herd" "$CONF" >"$tmp" && cat "$tmp" >"$CONF" && rm -f "$tmp"
			info "removed from $CONF"
		fi
	fi
	state="${XDG_STATE_HOME:-$HOME/.local/state}/herd"
	if [ -d "$state" ] && ask "Delete herd's logs in $state?" y; then
		rm -rf "$state"
		info "deleted $state"
	fi
	if [ -d "$DIR" ] && ask "Delete $DIR?" y; then
		rm -rf "$DIR"
		info "deleted $DIR"
	fi
	info "Done. Restart tmux (or start a new server) to drop the key bindings and status segment."
	exit 0
fi

# --- 1. requirements ------------------------------------------------------
step "Checking requirements"
case "$(uname -s)" in
Linux | Darwin) ;;
*) die "only Linux and macOS are supported" ;;
esac
command -v tmux >/dev/null || die "tmux is not installed"
command -v git >/dev/null || die "git is not installed"
ver="$(tmux -V | sed -E 's/^tmux (next-)?([0-9]+)\.([0-9]+).*/\2 \3/')"
read -r major minor <<<"$ver"
if [ "${major:-0}" -lt 3 ] || { [ "$major" -eq 3 ] && [ "${minor:-0}" -lt 3 ]; }; then
	die "tmux 3.3 or newer is required (found $(tmux -V))"
fi
info "$(tmux -V), git $(git --version | awk '{print $3}')"
command -v fzf >/dev/null || warn "fzf not found: the picker (prefix + a) falls back to a numbered list"

# --- 2. plugin files ------------------------------------------------------
step "Installing the plugin to $DIR"
if [ -d "$DIR/.git" ]; then
	git -C "$DIR" pull --ff-only --quiet && info "updated to $(git -C "$DIR" rev-parse --short HEAD)"
elif [ -e "$DIR" ]; then
	die "$DIR exists but is not a git checkout; move it away and re-run"
else
	mkdir -p "$(dirname "$DIR")"
	git clone --quiet --depth 1 --branch "$BRANCH" "$REPO_URL" "$DIR"
	info "cloned $(git -C "$DIR" rev-parse --short HEAD)"
fi

# --- 3. binary ------------------------------------------------------------
step "Installing the herd binary"
"$DIR/scripts/install-binary.sh"
info "$("$HERD" version) at $HERD"

# --- 4. tmux config -------------------------------------------------------
step "Adding the plugin to $CONF"
if [ -f "$CONF" ] && grep -q "tmux-agent-herd" "$CONF"; then
	info "already present"
else
	if uses_tpm; then
		line="set -g @plugin '$PLUGIN_SPEC'"
		info "you use TPM; adding: $line"
	else
		line="run-shell '$DIR/agent-herd.tmux'"
		info "adding: $line"
	fi
	if ask "Edit $CONF?" y; then
		mkdir -p "$(dirname "$CONF")"
		backup "$CONF"
		touch "$CONF"
		if uses_tpm && grep -Eq "^[[:space:]]*run(-shell)?[[:space:]].*tpm/tpm" "$CONF"; then
			# @plugin lines must come before TPM's run line.
			tmp="$(mktemp)"
			awk -v l="$line" '!done && /^[[:space:]]*run(-shell)?[[:space:]].*tpm\/tpm/ { print l; done=1 } { print }' "$CONF" >"$tmp"
			cat "$tmp" >"$CONF" && rm -f "$tmp"
		else
			printf '\n# tmux-agent-herd: agent state in tmux (https://github.com/kashym/tmux-agent-herd)\n%s\n' "$line" >>"$CONF"
		fi
		info "updated $CONF"
	else
		warn "skipped; add this line yourself: $line"
	fi
fi
if tmux_running; then
	tmux run-shell "$DIR/agent-herd.tmux" && info "loaded into your running tmux server"
else
	info "tmux is not running; the plugin loads the next time you start tmux"
fi

# --- 5. agents ------------------------------------------------------------
if [ "$AGENTS" = 1 ]; then
	step "Connecting your agent CLIs"
	"$HERD" setup --list | sed 's/^/    /'
	if "$HERD" setup --list | awk 'NR>1 && $2=="yes" {f=1} END {exit !f}'; then
		info "Each change below is shown as a diff and needs a yes; files are backed up."
		if [ "$YES" = 1 ]; then "$HERD" setup all --yes || warn "some harnesses failed; see above"; else "$HERD" setup all || warn "some harnesses failed; see above"; fi
	else
		info "no supported agent CLI found; screen detection still works. Run 'herd setup <name>' later."
	fi
fi

# --- 6. skill -------------------------------------------------------------
if [ "$SKILL" = 1 ] && [ "$AGENTS" = 1 ]; then
	step "Agent skill"
	info "The herd skill teaches your agents to start and coordinate other agents in tmux."
	if ask "Install it for your agent CLIs?" y; then
		"$HERD" install-skill 2>&1 | sed 's/^/    /' || warn "no harness with skills support found"
	fi
fi

step "Done"
cat <<EOF
    In tmux:
      prefix + a    pick an agent (blocked ones first)
      prefix + B    jump to the agent waiting on you
      prefix + D    jump to the agent that just finished

    From any pane:  herd ls        herd help
    Uninstall:      bash $DIR/install.sh --uninstall

    New panes have 'herd' on PATH; in older ones use $HERD
EOF
