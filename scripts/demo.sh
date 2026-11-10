#!/usr/bin/env bash
# Demo: a throwaway tmux server with fake agents in every state and the herd
# sidebar open. Your own tmux server and agent configs are not touched.
#
#   scripts/demo.sh            start it and attach (detach or exit to clean up)
#   HERD_DEMO_ATTACH=0 scripts/demo.sh   start it in the background only
#
# Inside: prefix is C-b. Click sidebar rows, or j/k + Enter. C-b d to leave.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HERD="$ROOT/bin/herd"
[ -x "$HERD" ] || "$ROOT/scripts/install-binary.sh"
SOCK_NAME="herd-demo"
T=(tmux -L "$SOCK_NAME" -f /dev/null)
TMP="$(mktemp -d "${TMPDIR:-/tmp}/herd-demo-XXXX")"
export XDG_STATE_HOME="$TMP/state"

"${T[@]}" kill-server 2>/dev/null || true

# Fake agents: print a screen, then idle. Named like real agent CLIs so herd
# detects them; they run nothing else.
mkfake() {
	local name="$1" body="$2"
	printf '#!/usr/bin/env bash\nclear\nprintf %%s "%s"\nwhile :; do sleep 60; done\n' "$body" >"$TMP/$name"
	chmod +x "$TMP/$name"
}
mkfake codex  $'> clean up the build output\n\n  I will remove the dist directory.\n\n  $ rm -rf dist\n\n  Allow this command?  (y) yes   (n) no\n'
mkfake claude $'> refactor the parser into its own package\n\n  Reading internal/parse/parse.go ...\n  Editing internal/parse/lexer.go ...\n\n  * Working (14s, esc to interrupt)\n'
mkfake pi     $'> write tests for the config loader\n\n  Added 6 tests in config_test.go. All pass.\n\n> \n'
mkfake opencode $'> summarise open issues\n\n  Error: 402 out of credits\n\n> \n'

W=160 H=40
"${T[@]}" new-session -d -s work -n api -x $W -y $H "$TMP/codex"
"${T[@]}" set -g mouse on
"${T[@]}" set -g status-style "bg=colour235,fg=colour250"
"${T[@]}" set -g @herd-sidebar on
c1="$("${T[@]}" display -p -t work:api '#{pane_id}')"
c2="$("${T[@]}" split-window -d -P -F '#{pane_id}' -t work:api "$TMP/claude")"
p1="$("${T[@]}" new-window -d -P -F '#{pane_id}' -t work: -n tests "$TMP/pi")"
"${T[@]}" new-session -d -s side -n research -x $W -y $H "$TMP/opencode"
o1="$("${T[@]}" display -p -t side:research '#{pane_id}')"
c3="$("${T[@]}" split-window -d -P -F '#{pane_id}' -t side:research "$TMP/claude")"

"${T[@]}" run-shell "$ROOT/agent-herd.tmux"
sleep 1.5

sock="$("${T[@]}" display -p '#{socket_path}')"
hook() { TMUX="$sock,1,0" TMUX_PANE="$1" "$ROOT/scripts/herd-hook" "${@:2}" </dev/null; }
hook "$c1" --agent codex --kind permission --msg "Bash: rm -rf dist" blocked
hook "$c2" --agent claude --msg Edit working
hook "$p1" --agent pi done
hook "$o1" --agent opencode --msg "402: out of credits" error
hook "$c3" --agent claude --msg WebFetch working
"${T[@]}" select-pane -t "$c2"

cleanup() {
	"${T[@]}" kill-server 2>/dev/null || true
	rm -rf "$TMP"
}
if [ "${HERD_DEMO_ATTACH:-1}" = 1 ]; then
	trap cleanup EXIT
	"${T[@]}" attach -t work
else
	echo "demo running: tmux -L $SOCK_NAME attach -t work   (clean up: tmux -L $SOCK_NAME kill-server; rm -rf $TMP)"
fi
