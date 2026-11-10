package state

import (
	"fmt"
	"strings"
)

// Glyph and colour per state, used by the status segment, badges and picker.
var Style = map[State]struct{ Glyph, Color string }{
	Blocked: {"●", "red"},
	Error:   {"✗", "magenta"},
	Done:    {"✓", "green"},
	Working: {"◐", "yellow"},
	Idle:    {"○", "colour244"},
}

// StatusLine renders the @herd_status segment, e.g.
// "#[fg=red]● 2 blocked#[default]  #[fg=yellow]◐ 3 working#[default]".
// Idle panes are not counted. Returns "" when nothing needs attention.
func StatusLine(recs map[string]Record) string {
	counts := map[State]int{}
	for _, r := range recs {
		counts[r.State]++
	}
	var parts []string
	for _, s := range []State{Blocked, Error, Working, Done} {
		if n := counts[s]; n > 0 {
			st := Style[s]
			parts = append(parts, fmt.Sprintf("#[fg=%s]%s %d %s#[default]", st.Color, st.Glyph, n, s))
		}
	}
	return strings.Join(parts, "  ")
}

// Worst returns the most severe state in recs.
func Worst(recs []Record) State {
	w := None
	for _, r := range recs {
		if Severity(r.State) > Severity(w) {
			w = r.State
		}
	}
	return w
}

// Badge renders a coloured dot for a window's worst state ("" for none).
func Badge(s State) string {
	st, ok := Style[s]
	if !ok {
		return ""
	}
	return fmt.Sprintf("#[fg=%s]%s#[default]", st.Color, st.Glyph)
}

// Sanitize makes a message safe to store in a tmux option that is displayed
// in formats: strips control characters, escapes '#', and truncates.
func Sanitize(msg string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range msg {
		if n >= max {
			b.WriteString("…")
			break
		}
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f:
			continue
		case r == '#':
			b.WriteString("##")
		default:
			b.WriteRune(r)
		}
		n++
	}
	return strings.TrimSpace(b.String())
}
