#!/usr/bin/env bash
# tmux-agent-herd: TPM entry point.
#
# Principles: bind one key (and never one that is already taken), keep
# state in @herd_* options for your own formats, only touch your status line
# where you ask (#{herd}, #{herd_badge}) or by the documented default, and
# record everything so `herd doctor` can show it.

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
	# Let agents and scripts in new panes call `herd` directly (@herd-path off
	# to skip; HERD_BIN always points at the binary).
	tmux set-environment -g HERD_BIN "$HERD"
	tmux set-environment -g HERD_HOOK "$CURRENT_DIR/scripts/herd-hook"
	is_on "$(get_opt @herd-path on)" || return 0
	local path
	path="$(tmux show-environment -g PATH 2>/dev/null)"
	path="${path#PATH=}"
	[ "$path" = "-PATH" ] && path=""
	[ -n "$path" ] || path="$PATH"
	case ":$path:" in
	*":$CURRENT_DIR/bin:"*) ;;
	*) tmux set-environment -g PATH "$path:$CURRENT_DIR/bin" ;;
	esac
}

# --- keys -------------------------------------------------------------------
BOUND=()
CONFLICTS=()

# The binding for KEY in the prefix table, if any. (Filters the full list:
# tmux 3.2 has no `list-keys -T prefix KEY`.)
key_owner() {
	tmux list-keys -T prefix 2>/dev/null | awk -v k="$1" '{
		for (i = 1; i < NF; i++) if ($i == "prefix") { if ($(i + 1) == k) { print; exit } break }
	}'
}

# tmux >= 3.3 supports popup titles.
popup_title() {
	local v
	v="$(tmux -V | sed -E 's/^tmux (next-)?([0-9]+)\.([0-9]+).*/\2\3/')"
	[ "${v:-0}" -ge 33 ] && printf '%s' "-T"
}

# bind_herd KEY LABEL tmux-command...  — binds only if KEY is free or ours.
bind_herd() {
	local key="$1" label="$2" owner
	shift 2
	{ [ -z "$key" ] || [ "$key" = off ]; } && return 0
	owner="$(key_owner "$key")"
	if [ -n "$owner" ] && ! printf '%s' "$owner" | grep -q 'herd'; then
		CONFLICTS+=("prefix+$key")
		return 0
	fi
	tmux bind-key "$key" "$@"
	BOUND+=("prefix+$key=$label")
}

# Drop herd bindings left by older versions on keys herd no longer uses, and
# give tmux's default back where there was one.
release_old_key() {
	local key="$1" owner
	owner="$(key_owner "$key")"
	printf '%s' "$owner" | grep -q 'herd' || return 0
	tmux unbind-key "$key"
	case "$key" in
	D) tmux bind-key D choose-client -Z ;;
	esac
}

setup_keys() {
	local herd mkey bkey dkey skey pkey w
	herd="$(q "$HERD")"
	mkey="$(get_opt @herd-key a)"
	bkey="$(get_opt @herd-blocked-key off)"
	dkey="$(get_opt @herd-done-key off)"
	skey="$(get_opt @herd-sidebar-key off)"
	pkey="$(get_opt @herd-picker-key off)"
	w="$(get_opt @herd-mode-width 32)"

	for k in a B D S; do
		case " $mkey $bkey $dkey $skey $pkey " in
		*" $k "*) ;;
		*) release_old_key "$k" ;;
		esac
	done

	local title=() pt
	pt="$(popup_title)"
	[ -n "$pt" ] && title=("$pt" ' herd ')
	bind_herd "$mkey" "herd_mode" display-popup -E -x 0 -y 0 -w "$((w + 2))" -h 100% "${title[@]}" \
		"$herd mode --client '#{client_name}'"
	bind_herd "$bkey" "jump_to_blocked" run-shell -b "$herd jump blocked --client '#{client_name}'"
	bind_herd "$dkey" "jump_to_done" run-shell -b "$herd jump done --client '#{client_name}'"
	bind_herd "$skey" "toggle_sidebar" run-shell -b "$herd sidebar --toggle --window '#{window_id}'"
	bind_herd "$pkey" "fzf_picker" display-popup -E -w 90% -h 80% "${title[@]}" "$herd pick --client '#{client_name}'"

	tmux set-option -g @herd_keys "${BOUND[*]}"
	tmux set-option -g @herd_conflicts "${CONFLICTS[*]}"
	if [ "${#CONFLICTS[@]}" -gt 0 ]; then
		tmux display-message -d 8000 \
			"herd: ${CONFLICTS[*]} already bound, left alone. Pick a key with @herd-key (herd doctor)" 2>/dev/null
	fi
	return 0
}

# --- hooks -------------------------------------------------------------------
setup_hooks() {
	local herd
	herd="$(q "$HERD")"
	# Fixed array indexes so re-sourcing replaces our hooks, never the user's.
	tmux set-hook -g 'client-attached[4210]' "run-shell -b \"$herd daemon --ensure; $herd _welcome\""
	tmux set-hook -g 'pane-focus-in[4210]' "run-shell -b \"$herd _focus #{pane_id}\""
}

# --- status line ---------------------------------------------------------------
STATUS_SEG='#{?#{@herd_status},#{E:@herd_status} ,}'
BADGE_SEG='#{?@herd_window_badge,#{E:@herd_window_badge} ,}'

# replace_placeholder OPTION PLACEHOLDER REPLACEMENT — returns 0 if found.
replace_placeholder() {
	local v
	v="$(tmux show-option -gqv "$1")"
	case "$v" in
	*"$2"*)
		tmux set-option -g "$1" "${v//"$2"/$3}"
		return 0
		;;
	esac
	return 1
}

setup_status() {
	local found=0 sr len opt f mode=off
	# 1. #{herd} where you put it (status-left or status-right).
	replace_placeholder status-right '#{herd}' "$STATUS_SEG" && found=1
	replace_placeholder status-left '#{herd}' "$STATUS_SEG" && found=1
	if [ "$found" = 1 ]; then
		mode=placeholder
	elif is_on "$(get_opt @herd-status on)"; then
		# 2. Default: append to status-right once.
		sr="$(tmux show-option -gqv status-right)"
		case "$sr" in
		*"@herd_status"*) mode=present ;;
		*)
			tmux set-option -g status-right "$sr $STATUS_SEG"
			len="$(tmux show-option -gqv status-right-length)"
			[ "${len:-0}" -lt 80 ] && tmux set-option -g status-right-length 80
			mode=appended
			;;
		esac
	fi
	tmux set-option -g @herd_status_mode "$mode"

	found=0
	for opt in window-status-format window-status-current-format; do
		replace_placeholder "$opt" '#{herd_badge}' "$BADGE_SEG" && found=1
	done
	mode=off
	if [ "$found" = 1 ]; then
		mode=placeholder
	elif is_on "$(get_opt @herd-window-badge on)"; then
		mode=prefixed
		for opt in window-status-format window-status-current-format; do
			f="$(tmux show-option -gqv "$opt")"
			case "$f" in
			*"@herd_window_badge"*) ;;
			*) tmux set-option -g "$opt" "$BADGE_SEG$f" ;;
			esac
		done
	fi
	tmux set-option -g @herd_badge_mode "$mode"

	if is_on "$(get_opt @herd-border off)"; then
		tmux set-option -g pane-border-status top
		tmux set-option -g pane-border-format \
			' #{?@herd_agent,#{E:@herd_border},#{pane_index} #{pane_current_command}} '
		tmux set-option -g @herd_border \
			'#{@herd_agent} · #{?#{==:#{@herd_state},blocked},#[fg=red bold],#{?#{==:#{@herd_state},done},#[fg=green],#{?#{==:#{@herd_state},working},#[fg=yellow],}}}#{@herd_state}#[default]#{?@herd_msg, · #{@herd_msg},}'
	fi
}

# --- daemon, sidebars, welcome --------------------------------------------------
after_binary() {
	# Runs once the binary is in place.
	local sock="$1"
	"$HERD" -S "$sock" daemon --ensure >/dev/null 2>&1 ||
		tmux display-message "tmux-agent-herd: could not start herdd (see ~/.local/state/herd/herdd.log)"
	if is_on "$(get_opt @herd-sidebar off)"; then
		"$HERD" -S "$sock" mode --pin on >/dev/null 2>&1
	else
		tmux set-hook -gu 'after-new-window[4210]' 2>/dev/null
		tmux set-hook -gu 'after-new-session[4210]' 2>/dev/null
	fi
	"$HERD" -S "$sock" _welcome >/dev/null 2>&1
	return 0
}

start() {
	local sock
	sock="$(tmux display-message -p '#{socket_path}')"
	if [ -x "$HERD" ] && [ "$("$HERD" version 2>/dev/null)" = "herd $(cat "$CURRENT_DIR/VERSION" 2>/dev/null)" ]; then
		after_binary "$sock"
	else
		# First run or plugin update: fetch/build the binary in the background.
		tmux run-shell -b "$(q "$CURRENT_DIR/scripts/install-binary.sh") && $(q "$CURRENT_DIR/agent-herd.tmux") --after-binary $(q "$sock") || tmux display-message 'tmux-agent-herd: binary install failed; run scripts/install-binary.sh'"
	fi
}

if [ "${1:-}" = "--after-binary" ]; then
	after_binary "$2"
	exit 0
fi

setup_path
setup_keys
setup_hooks
setup_status
start
