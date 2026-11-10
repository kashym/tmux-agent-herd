#!/usr/bin/env bash
# Build docs/social-preview.png (1280x640) from docs/demo-still.png.
# Upload it in the repo's Settings → General → Social preview.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STILL="$ROOT/docs/demo-pinned.png"
[ -f "$STILL" ] || { echo "run scripts/media/record-demo.sh first" >&2; exit 1; }
CHROME="$(command -v chromium || command -v chromium-browser || command -v google-chrome)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cat >"$TMP/card.html" <<EOF
<!doctype html><meta charset="utf-8"><style>
html,body{margin:0;width:1280px;height:640px;overflow:hidden;background:#13141f}
body{display:flex;font-family:"Inter","Noto Sans","DejaVu Sans",sans-serif;color:#c0caf5}
.text{width:520px;padding:64px 0 0 64px;box-sizing:border-box}
h1{margin:0;font:700 46px/1.1 "JetBrainsMono Nerd Font","JetBrains Mono",monospace;color:#e6ebff;letter-spacing:-1px}
h1 span{color:#7aa2f7}
p{margin:26px 0 0;font-size:27px;line-height:1.35;color:#c0caf5}
.states{margin-top:30px;font:500 21px/1.9 "DejaVu Sans Mono",monospace}
.install{margin-top:40px;display:inline-block;padding:12px 16px;border-radius:10px;background:#1f2133;border:1px solid #2c3050;
  font:15px/1.4 "JetBrainsMono Nerd Font","JetBrains Mono",monospace;color:#9aa5ce}
.install b{color:#7aa2f7;font-weight:500}
.b{color:#ff6b81}.w{color:#e0af68}.d{color:#9ece6a}
.agents{position:absolute;left:64px;bottom:54px;width:430px;font-size:17px;line-height:1.5;color:#7a82a8}
.shot{position:absolute;right:-6px;top:56px;width:760px;height:528px;border-radius:14px 0 0 14px;overflow:hidden;
  box-shadow:0 20px 60px rgba(0,0,0,.55);border:1px solid #2c3050;border-right:0}
.shot img{width:1188px;height:672px;transform:scale(.8);transform-origin:0 0;margin:-8px 0 0 -8px}
</style>
<div class="text">
  <h1>tmux-agent-<span>herd</span></h1>
  <p>Know which of your coding agents needs you, without leaving tmux.</p>
  <div class="states"><span class="b">● blocked</span> &nbsp;<span class="w">◐ working</span> &nbsp;<span class="d">✓ done</span></div>
  <div class="install">set -g @plugin '<b>kashym/tmux-agent-herd</b>'</div>
</div>
<div class="agents">Works with Claude Code, Codex, Gemini CLI,<br>Copilot CLI, OpenCode, pi and aider</div>
<div class="shot"><img src="file://$STILL"></div>
EOF
"$CHROME" --headless=new --no-sandbox --disable-gpu --hide-scrollbars --allow-file-access-from-files \
	--window-size=1280,640 --screenshot="$ROOT/docs/social-preview.png" "file://$TMP/card.html" >/dev/null 2>&1
ls -l "$ROOT/docs/social-preview.png"
