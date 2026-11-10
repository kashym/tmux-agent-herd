#!/usr/bin/env python3
"""Render a `tmux capture-pane -e -p` dump as a standalone HTML page.

Usage: ansi2html.py INPUT.ansi [COLS ROWS] > OUTPUT.html
Used by record-demo.sh to turn terminal frames into images.
"""
import html
import re
import sys

# A calm dark palette (black, red, green, yellow, blue, magenta, cyan, white, + bright).
PALETTE = ["#1b1d2b", "#ff6b81", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#c0caf5",
           "#565f89", "#ff7a93", "#b9f27c", "#ff9e64", "#7da6ff", "#c39ff2", "#0db9d7", "#e6ebff"]
FG, BG = "#c0caf5", "#1b1d2b"


def c256(n):
    if n < 16:
        return PALETTE[n]
    if n < 232:
        n -= 16
        v = [0, 95, 135, 175, 215, 255]
        return "#%02x%02x%02x" % (v[n // 36], v[(n // 6) % 6], v[n % 6])
    g = 8 + (n - 232) * 10
    return "#%02x%02x%02x" % (g, g, g)


def convert(text, cols, rows):
    lines = text.rstrip("\n").split("\n")[:rows]
    lines += [""] * (rows - len(lines))
    out = []
    for line in lines:
        st = dict(fg=None, bg=None, bold=False, dim=False, rev=False)
        row = []
        width = 0
        for part in re.split(r"(\x1b\[[0-9;:]*m)", line):
            if part.startswith("\x1b["):
                codes = [int(x) if x.isdigit() else 0 for x in re.split("[;:]", part[2:-1])] or [0]
                i = 0
                while i < len(codes):
                    c = codes[i]
                    if c == 0:
                        st = dict(fg=None, bg=None, bold=False, dim=False, rev=False)
                    elif c == 1:
                        st["bold"] = True
                    elif c == 2:
                        st["dim"] = True
                    elif c == 7:
                        st["rev"] = True
                    elif c == 22:
                        st["bold"] = st["dim"] = False
                    elif c == 27:
                        st["rev"] = False
                    elif 30 <= c <= 37:
                        st["fg"] = PALETTE[c - 30]
                    elif 90 <= c <= 97:
                        st["fg"] = PALETTE[c - 90 + 8]
                    elif c == 39:
                        st["fg"] = None
                    elif 40 <= c <= 47:
                        st["bg"] = PALETTE[c - 40]
                    elif 100 <= c <= 107:
                        st["bg"] = PALETTE[c - 100 + 8]
                    elif c == 49:
                        st["bg"] = None
                    elif c in (38, 48) and i + 2 < len(codes) and codes[i + 1] == 5:
                        st["fg" if c == 38 else "bg"] = c256(codes[i + 2])
                        i += 2
                    elif c in (38, 48) and i + 4 < len(codes) and codes[i + 1] == 2:
                        st["fg" if c == 38 else "bg"] = "#%02x%02x%02x" % tuple(codes[i + 2:i + 5])
                        i += 4
                    i += 1
                continue
            if not part:
                continue
            fg, bg = st["fg"] or FG, st["bg"] or BG
            if st["rev"]:
                fg, bg = (st["bg"] or BG), (st["fg"] or FG)
            css = f"color:{fg};background:{bg};"
            if st["bold"]:
                css += "font-weight:700;"
            if st["dim"]:
                css += "opacity:.55;"
            row.append(f'<span style="{css}">{html.escape(part)}</span>')
            width += len(part)
        if width < cols:
            row.append(" " * (cols - width))
        out.append("".join(row))
    return out


def main():
    src = open(sys.argv[1], encoding="utf-8", errors="replace").read()
    cols = int(sys.argv[2]) if len(sys.argv) > 2 else 120
    rows = int(sys.argv[3]) if len(sys.argv) > 3 else 32
    body = "\n".join(convert(src, cols, rows))
    print(f"""<!doctype html><meta charset="utf-8"><style>
html,body{{margin:0;background:{BG}}}
@font-face{{font-family:"HerdGlyphs";src:local("DejaVu Sans Mono");unicode-range:U+25CB-25D1,U+2713,U+2717,U+258D}}
pre{{margin:0;padding:16px 18px;font:16px/20px "HerdGlyphs","JetBrainsMono Nerd Font","JetBrains Mono","DejaVu Sans Mono",monospace;
color:{FG};background:{BG};white-space:pre;font-variant-ligatures:none}}
</style><pre>{body}</pre>""")


if __name__ == "__main__":
    main()
