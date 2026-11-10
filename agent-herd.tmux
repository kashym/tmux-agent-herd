#!/usr/bin/env bash
# tmux-agent-herd: TPM entry point.
# Reads @herd-* options, sets keybindings, hooks and formats, and starts one
# herdd per tmux server (building/downloading the binary on first run).

CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HERD="$CURRENT_DIR/bin/herd"

get_opt() {
	local v
	v="$(tmux show-option -gqv "$1")"
	printf '%s' "${v:-$2}"
}

q() { printf "'%s'" "${1//\'/\'\\\'\'}"; }

is_on() { case "$1" in on|yes|true|1) return 0 ;; esac; return 1; }

setup_path() {
	# Let agents and scripts in new panes call `herd` directly.
	local path
	path="$(tmux show-environment -g PATH 2>/dev/null)"
	path="${path#PATH=}"
	[ "$path" = "-PATH" ] && path=""
	[ -n "$path" ] || path="$PATH"
	case ":$path:" in
	*":$CURRENT_DIR/bin:"*) ;;
	*) tmux set-environment -g PATH "$path:$CURRENT_DIR/bin" ;;
	esac
	tmux set-environment -g HERD_BIN "$HERD"
}

setup_keys() {
	local herd key bkey dkey skey
	herd="$(q "$HERD")"
	key="$(get_opt @herd-picker-key a)"
	bkey="$(get_opt @herd-blocked-key B)"
	dkey="$(get_opt @herd-done-key D)"
	skey="$(get_opt @herd-sidebar-key S)"
	[ -n "$key" ] && [ "$key" != off ] &&
		tmux bind-key "$key" display-popup -E -w 90% -h 80% -T ' herd ' "$herd pick --client '#{client_name}'"
	[ -n "$bkey" ] && [ "$bkey" != off ] &&
		tmux bind-key "$bkey" run-shell -b "$herd jump blocked --client '#{client_name}'"
	[ -n "$dkey" ] && [ "$dkey" != off ] &&
		tmux bind-key "$dkey" run-shell -b "$herd jump done --client '#{client_name}'"
	[ -n "$skey" ] && [ "$skey" != off ] &&
		tmux bind-key "$skey" run-shell -b "$herd sidebar --toggle --window '#{window_id}'"
	return 0
}

setup_sidebar() {
	# @herd-sidebar on: keep a sidebar in every window, like a dashboard.
	local herd w
	herd="$(q "$HERD")"
	if is_on "$(get_opt @herd-sidebar off)"; then
		tmux set-hook -g 'after-new-window[4210]' "run-shell -b \"$herd sidebar --ensure --window #{window_id}\""
		tmux set-hook -g 'after-new-session[4210]' "run-shell -b \"$herd sidebar --ensure --window #{window_id}\""
		for w in $(tmux list-windows -a -F '#{window_id}'); do
			tmux run-shell -b "$herd sidebar --ensure --window $w"
		done
	else
		tmux set-hook -gu 'after-new-window[4210]' 2>/dev/null
		tmux set-hook -gu 'after-new-session[4210]' 2>/dev/null
	fi
	return 0
}

setup_hooks() {
	local herd
	herd="$(q "$HERD")"
	# Fixed array indexes so re-sourcing replaces our hooks, never the user's.
	tmux set-hook -g 'client-attached[4210]' "run-shell -b \"$herd daemon --ensure\""
	tmux set-hook -g 'pane-focus-in[4210]' "run-shell -b \"$herd _focus #{pane_id}\""
}

setup_status() {
	local seg='#{E:@herd_status}' sr len
	if is_on "$(get_opt @herd-status on)"; then
		sr="$(tmux show-option -gqv status-right)"
		case "$sr" in
		*"$seg"*) ;;
		*) tmux set-option -g status-right "$sr #{?#{@herd_status},$seg ,}" ;;
		esac
		len="$(tmux show-option -gqv status-right-length)"
		[ "${len:-0}" -lt 100 ] && tmux set-option -g status-right-length 100
	fi

	local badge='#{?@herd_window_badge,#{E:@herd_window_badge} ,}' f opt
	if is_on "$(get_opt @herd-window-badge on)"; then
		for opt in window-status-format window-status-current-format; do
			f="$(tmux show-option -gqv "$opt")"
			case "$f" in
			*"@herd_window_badge"*) ;;
			*) tmux set-option -g "$opt" "$badge$f" ;;
			esac
		done
	fi

	if is_on "$(get_opt @herd-border off)"; then
		tmux set-option -g pane-border-status top
		tmux set-option -g pane-border-format \
			' #{?@herd_agent,#{E:@herd_border},#{pane_index} #{pane_current_command}} '
		tmux set-option -g @herd_border \
			'#{@herd_agent} · #{?#{==:#{@herd_state},blocked},#[fg=red bold],#{?#{==:#{@herd_state},done},#[fg=green],#{?#{==:#{@herd_state},working},#[fg=yellow],}}}#{@herd_state}#[default]#{?@herd_msg, · #{@herd_msg},}'
	fi
}

start_daemon() {
	local sock herd
	sock="$(tmux display-message -p '#{socket_path}')"
	herd="$(q "$HERD")"
	if [ -x "$HERD" ] && "$HERD" version >/dev/null 2>&1 &&
		[ "$("$HERD" version)" = "herd $(cat "$CURRENT_DIR/VERSION" 2>/dev/null)" ]; then
		"$HERD" -S "$sock" daemon --ensure >/dev/null 2>&1 ||
			tmux display-message "tmux-agent-herd: could not start herdd (see ~/.local/state/herd/herdd.log)"
	else
		# First run or plugin update: fetch/build the binary in the background.
		tmux run-shell -b "$(q "$CURRENT_DIR/scripts/install-binary.sh") && $herd -S $(q "$sock") daemon --ensure || tmux display-message 'tmux-agent-herd: binary install failed; run scripts/install-binary.sh'"
	fi
}

main() {
	setup_path
	setup_keys
	setup_hooks
	setup_status
	start_daemon
	setup_sidebar
}
main
