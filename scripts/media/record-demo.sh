#!/usr/bin/env bash
# Record docs/demo.gif, docs/demo-still.png (herd mode) and
# docs/demo-pinned.png (pinned sidebar) from a scripted story played
# on a throwaway tmux server. Your tmux and agent configs are not touched.
#
#   scripts/media/record-demo.sh
#
# Needs: tmux, python3, chromium (or google-chrome), ffmpeg, and bin/herd
# (go build -o bin/herd ./cmd/herd). Takes about two minutes.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MEDIA="$ROOT/scripts/media"
HERD="$ROOT/bin/herd"
COLS=120 ROWS=32
[ -x "$HERD" ] || { echo "build bin/herd first: go build -o bin/herd ./cmd/herd" >&2; exit 1; }
CHROME="$(command -v chromium || command -v chromium-browser || command -v google-chrome || true)"
[ -n "$CHROME" ] || { echo "needs chromium or google-chrome" >&2; exit 1; }
command -v ffmpeg >/dev/null || { echo "needs ffmpeg" >&2; exit 1; }

TMP="$(mktemp -d "${TMPDIR:-/tmp}/herd-rec-XXXX")"
IN=(tmux -L herd-rec -f "$TMP/tmux.conf")
OUT=(tmux -L herd-rec-out -f /dev/null)
export XDG_STATE_HOME="$TMP/state"
cleanup() {
	"${OUT[@]}" kill-server 2>/dev/null || true
	"${IN[@]}" kill-server 2>/dev/null || true
	rm -rf "$TMP"
}
trap cleanup EXIT
mkdir -p "$TMP/bin" "$TMP/screens" "$TMP/frames" "$XDG_STATE_HOME/herd"
touch "$XDG_STATE_HOME/herd/welcomed" # no welcome banner in the recording

# --- the cast: fake agents that redraw a screen file when it changes -------
for name in codex claude pi opencode notes; do
	cat >"$TMP/bin/$name" <<'EOF'
#!/usr/bin/env bash
f="$FAKE_SCREEN"; last=""
printf '\e[?25l'
while :; do cur="$(cat "$f" 2>/dev/null)"; if [ "$cur" != "$last" ]; then printf '\e[H\e[2J%s' "$cur"; last="$cur"; fi; sleep 0.05; done
EOF
	chmod +x "$TMP/bin/$name"
done
D=$'\e[2m' B=$'\e[1m' G=$'\e[32m' Y=$'\e[33m' R=$'\e[31m' C=$'\e[36m' M=$'\e[35m' N=$'\e[0m'
screen() { printf '%s' "$2" >"$TMP/screens/$1"; }

screen notes "${D}  1 ${N}${B}# Launch checklist${N}
${D}  2 ${N}
${D}  3 ${N}Agents are busy in other windows while I write this. The status line says how
${D}  4 ${N}many are working, blocked or done; window names get a dot for the most urgent.
${D}  5 ${N}
${D}  6 ${N}- [x] herd mode: one key shows every agent, the ones that need me first
${D}  7 ${N}- [x] sidebar: pin it to every window
${D}  8 ${N}- [ ] record the demo
${D}  9 ${N}- [ ] write the launch post
${D} 10 ${N}
${D} 11 ${N}${D}Next: approve whatever codex is asking for, then review the parser tests.${N}

${D}notes.md  11 lines${N}"
screen codex "${C}›${N} clean up the build output

${D}•${N} Listing build artifacts…

  ${Y}Working${N} ${D}(6s • esc to interrupt)${N}"
screen claude "${M}>${N} add tests for the parser

${G}●${N} Writing parser_test.go

${Y}✻ Thinking…${N} ${D}(12s · esc to interrupt)${N}"
screen pi "${B}>${N} split the router into modules

  editing router.go …
  editing routes/api.go …"
screen opencode "${B}>${N} summarise open issues

  12 open issues: 4 bugs, 8 features.

${B}>${N} "

cat >"$TMP/tmux.conf" <<EOF
set -g base-index 1
setw -g pane-base-index 1
set -g escape-time 0
set -g mouse on
set -g status-interval 1
set -g status-style 'bg=#232536,fg=#a9b1d6'
set -g status-left '#[fg=#1b1d2b,bg=#7aa2f7,bold] #S #[default] '
set -g status-right '#{herd}#[fg=#565f89]14:02 '
set -g status-right-length 90
set -g window-status-format '#{herd_badge}#[fg=#565f89]#I:#W '
set -g window-status-current-format '#{herd_badge}#[fg=#c0caf5,bold]#I:#W '
set -g pane-border-style 'fg=#3b3f5c'
set -g pane-active-border-style 'fg=#7aa2f7'
set -g popup-border-style 'fg=#7aa2f7'
set -g message-style 'bg=#232536,fg=#7aa2f7'
set -g @herd-notify off
set -g @herd-path off
EOF

# --- the set ---------------------------------------------------------------
agent() { # agent NAME TARGET-ARGS...
	local name="$1"
	shift
	"${IN[@]}" "$@" -P -F '#{pane_id}' -e "FAKE_SCREEN=$TMP/screens/$name" "$TMP/bin/$name"
}
"${IN[@]}" new-session -d -s work -n notes -x $COLS -y $ROWS -e "FAKE_SCREEN=$TMP/screens/notes" "$TMP/bin/notes"
codex="$(agent codex new-window -d -t work: -n api)"
claude="$(agent claude split-window -d -h -t "$codex")"
pi="$(agent pi new-window -d -t work: -n web)"
opencode="$(agent opencode new-window -d -t work: -n docs)"
"${IN[@]}" select-window -t work:notes
"${IN[@]}" run-shell "$ROOT/agent-herd.tmux"
sock="$("${IN[@]}" display -p '#{socket_path}')"
hook() { TMUX="$sock,1,0" TMUX_PANE="$1" "$ROOT/scripts/herd-hook" "${@:2}" </dev/null; }
hook "$codex" --agent codex --msg "list artifacts" working
hook "$claude" --agent claude --msg Edit working
hook "$pi" --agent pi --msg Edit working
sleep 2

# The viewer: a client attached through an outer tmux we can screenshot.
"${OUT[@]}" new-session -d -s out -x $COLS -y $ROWS \
	"env -u TMUX XDG_STATE_HOME=$XDG_STATE_HOME tmux -L herd-rec attach -t work"
"${OUT[@]}" set -g status off
sleep 1.5
key() { "${OUT[@]}" send-keys -t out: "$@"; }

# --- action ----------------------------------------------------------------
touch "$TMP/recording"
(
	i=0
	while [ -e "$TMP/recording" ]; do
		f="$TMP/frames/$(printf '%05d' $i)"
		"${OUT[@]}" capture-pane -e -p -t out: >"$f.ansi"
		date +%s.%N >"$f.t"
		i=$((i + 1))
		sleep 0.06
	done
) &
REC=$!

sleep 1.6 # everyone is working
screen codex "${C}›${N} clean up the build output

${D}•${N} I'll remove the old build directory.

  ${B}\$ rm -rf dist${N}

  Allow this command?
  ${C}› 1. Yes${N}   2. No"
hook "$codex" --agent codex --kind permission --msg "Bash: rm -rf dist" blocked
sleep 1.4
screen pi "${B}>${N} split the router into modules

  ${G}✓${N} router split into 3 modules, tests pass

${B}>${N} "
hook "$pi" --agent pi done
sleep 1.2
key C-b a # herd mode
sleep 0.7
date +%s.%N >"$TMP/still-at"
sleep 0.9
key j # to the blocked agent
sleep 0.7
key Enter # go there
sleep 1.2
screen codex "${C}›${N} clean up the build output

${D}•${N} I'll remove the old build directory.

  ${G}✓${N} removed dist/

  ${Y}Working${N} ${D}(1s • esc to interrupt)${N}"
hook "$codex" --agent codex --msg Bash working
sleep 1.0
screen claude "${M}>${N} add tests for the parser

${G}●${N} Added 8 tests; all pass.

${M}>${N} "
hook "$claude" --agent claude done
sleep 1.0
key C-b a
sleep 0.8
key p # pin: a sidebar in every window
sleep 1.2
screen opencode "${B}>${N} summarise open issues

  12 open issues: 4 bugs, 8 features.
  Write them to ISSUES.md?

  ${C}› 1. Yes${N}   2. No"
hook "$opencode" --agent opencode --kind permission --msg "write ISSUES.md" blocked
sleep 1.2
date +%s.%N >"$TMP/pinned-at" # the social preview frame
sleep 0.4
key C-b 3 # the sidebar follows you to every window
sleep 1.1
key C-b 4
sleep 1.0
rm -f "$TMP/recording"
wait $REC

# --- frames -> gif ------------------------------------------------------------
echo "rendering frames…"
python3 - "$TMP" "$MEDIA/ansi2html.py" "$CHROME" $COLS $ROWS <<'PY'
import glob, os, subprocess, sys
tmp, conv, chrome, cols, rows = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4]), int(sys.argv[5])
frames = sorted(glob.glob(f"{tmp}/frames/*.ansi"))
uniq = []  # (path, start time)
last = None
for f in frames:
    data = open(f, "rb").read()
    if data != last and data.strip():
        uniq.append((f, float(open(f[:-5] + ".t").read())))
        last = data
still_at = float(open(f"{tmp}/still-at").read())
pinned_at = float(open(f"{tmp}/pinned-at").read())
width, height = 1188, 672
os.makedirs(f"{tmp}/png", exist_ok=True)
lines, still, pinned = [], None, None
for i, (f, t) in enumerate(uniq):
    html = f"{tmp}/png/{i:04d}.html"
    png = f"{tmp}/png/{i:04d}.png"
    with open(html, "w") as h:
        subprocess.run(["python3", conv, f, str(cols), str(rows)], stdout=h, check=True)
    subprocess.run([chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--hide-scrollbars",
                    f"--window-size={width},{height}", f"--screenshot={png}", f"file://{html}"],
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
    dur = (uniq[i + 1][1] - t) if i + 1 < len(uniq) else 2.2
    lines.append(f"file '{png}'\nduration {max(dur, 0.05):.3f}\n")
    if t <= still_at:
        still = png
    if t <= pinned_at:
        pinned = png
lines.append(f"file '{uniq[-1][0] and png}'\n")
open(f"{tmp}/list.txt", "w").write("".join(lines))
open(f"{tmp}/still.txt", "w").write(still or png)
open(f"{tmp}/pinned.txt", "w").write(pinned or png)
print(f"{len(uniq)} distinct frames")
PY
mkdir -p "$ROOT/docs"
ffmpeg -loglevel error -y -f concat -safe 0 -i "$TMP/list.txt" \
	-vf "fps=12,split[a][b];[a]palettegen=max_colors=96:stats_mode=diff[p];[b][p]paletteuse=dither=none:diff_mode=rectangle" \
	-loop 0 "$ROOT/docs/demo.gif"
cp "$(cat "$TMP/still.txt")" "$ROOT/docs/demo-still.png"
cp "$(cat "$TMP/pinned.txt")" "$ROOT/docs/demo-pinned.png"
ls -l "$ROOT/docs/demo.gif" "$ROOT/docs/demo-still.png" "$ROOT/docs/demo-pinned.png"
