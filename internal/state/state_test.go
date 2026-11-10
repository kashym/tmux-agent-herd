package state

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

func at(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }

func eff(t *testing.T, tb *Table, id string, now time.Time) Record {
	t.Helper()
	p := tb.Pane(id)
	if p == nil {
		return Record{PaneID: id, State: None}
	}
	return tb.Effective(p, now)
}

func TestProcessDetectionShowsIdle(t *testing.T) {
	tb := New([]string{"claude", "codex"}, 10*time.Minute)
	tb.Observe([]PaneInfo{{ID: "%1", Command: "claude"}, {ID: "%2", Command: "bash"}}, at(0))
	if r := eff(t, tb, "%1", at(0)); r.State != Idle || r.Source != "process" || r.Agent != "claude" {
		t.Fatalf("got %+v", r)
	}
	if tb.Pane("%2") != nil {
		t.Fatal("non-agent pane should not be tracked")
	}
}

func TestHookBeatsScreenUntilStale(t *testing.T) {
	tb := New([]string{"claude"}, 10*time.Minute)
	tb.Observe([]PaneInfo{{ID: "%1", Command: "claude"}}, at(0))
	tb.ReportScreen("%1", Report{State: Blocked, Kind: "permission"}, at(1))
	tb.Report("%1", SrcHook, "claude", Report{State: Working, At: at(2)})
	if r := eff(t, tb, "%1", at(3)); r.State != Working || r.Source != "hook" {
		t.Fatalf("hook should win: %+v", r)
	}
	// 11 minutes later the hook is stale; screen wins again.
	if r := eff(t, tb, "%1", at(2+11*60)); r.State != Blocked || r.Source != "screen" || r.Kind != "permission" {
		t.Fatalf("screen should win after stale: %+v", r)
	}
}

func TestDoneDecaysOnFocus(t *testing.T) {
	tb := New([]string{"claude"}, 10*time.Minute)
	tb.Observe([]PaneInfo{{ID: "%1", Command: "claude", Focused: false}}, at(0))
	tb.Report("%1", SrcHook, "", Report{State: Done, At: at(5)})
	if r := eff(t, tb, "%1", at(6)); r.State != Done {
		t.Fatalf("want done, got %+v", r)
	}
	// Still focused-from-before panes keep done; a focus transition decays it.
	tb.Observe([]PaneInfo{{ID: "%1", Command: "claude", Focused: true}}, at(7))
	if r := eff(t, tb, "%1", at(7)); r.State != Idle {
		t.Fatalf("want idle after focus, got %+v", r)
	}
	// A new done after the focus shows again.
	tb.Report("%1", SrcHook, "", Report{State: Done, At: at(9)})
	if r := eff(t, tb, "%1", at(9)); r.State != Done {
		t.Fatalf("want done, got %+v", r)
	}
	tb.Focus("%1", at(10))
	if r := eff(t, tb, "%1", at(10)); r.State != Idle {
		t.Fatalf("want idle after focus hook, got %+v", r)
	}
}

func TestAgentExitForgetsPane(t *testing.T) {
	tb := New([]string{"claude"}, 10*time.Minute)
	tb.Observe([]PaneInfo{{ID: "%1", Command: "claude"}}, at(0))
	tb.Report("%1", SrcHook, "", Report{State: Working, At: at(1)})
	tb.Observe([]PaneInfo{{ID: "%1", Command: "zsh"}}, at(2))
	if tb.Pane("%1") != nil {
		t.Fatal("pane should be forgotten when agent exits to shell")
	}
}

func TestHookNamedAgentUnderOtherCommand(t *testing.T) {
	tb := New([]string{"claude"}, 10*time.Minute)
	tb.Report("%3", SrcHook, "claude", Report{State: Working, At: at(0)})
	tb.Observe([]PaneInfo{{ID: "%3", Command: "node"}}, at(1))
	if r := eff(t, tb, "%3", at(1)); r.State != Working || r.Agent != "claude" {
		t.Fatalf("got %+v", r)
	}
	// Pane disappears entirely.
	tb.Observe(nil, at(2))
	if tb.Pane("%3") != nil {
		t.Fatal("gone pane should be dropped")
	}
}

func TestSessionEndClears(t *testing.T) {
	tb := New([]string{"claude"}, 10*time.Minute)
	tb.Report("%3", SrcHook, "claude", Report{State: Working, At: at(0)})
	tb.Observe([]PaneInfo{{ID: "%3", Command: "node"}}, at(1))
	tb.Report("%3", SrcHook, "", Report{State: None, At: at(2)})
	if r := eff(t, tb, "%3", at(2)); r.State != None {
		t.Fatalf("want none, got %+v", r)
	}
}

func TestScreenWorkingToIdleIsDone(t *testing.T) {
	tb := New([]string{"aider"}, 10*time.Minute)
	tb.Observe([]PaneInfo{{ID: "%1", Command: "aider"}}, at(0))
	tb.ReportScreen("%1", Report{State: Idle}, at(1))
	if r := eff(t, tb, "%1", at(1)); r.State != Idle {
		t.Fatalf("got %+v", r)
	}
	tb.ReportScreen("%1", Report{State: Working}, at(2))
	tb.ReportScreen("%1", Report{State: Working}, at(3))
	if r := eff(t, tb, "%1", at(3)); r.State != Working || !r.UpdatedAt.Equal(at(2)) {
		t.Fatalf("working should keep first timestamp: %+v", r)
	}
	tb.ReportScreen("%1", Report{State: Idle}, at(4))
	tb.ReportScreen("%1", Report{State: Idle}, at(5))
	if r := eff(t, tb, "%1", at(5)); r.State != Done || !r.UpdatedAt.Equal(at(4)) {
		t.Fatalf("want sticky done at t=4, got %+v", r)
	}
}

func TestRecomputeChanges(t *testing.T) {
	tb := New([]string{"claude"}, 10*time.Minute)
	tb.Observe([]PaneInfo{{ID: "%1", Command: "claude"}}, at(0))
	prev, ch := tb.Recompute(at(0), nil)
	if len(ch) != 1 || ch[0].New.State != Idle || ch[0].Old.State != None {
		t.Fatalf("got %+v", ch)
	}
	prev, ch = tb.Recompute(at(1), prev)
	if len(ch) != 0 {
		t.Fatalf("no change expected, got %+v", ch)
	}
	tb.Observe(nil, at(2))
	_, ch = tb.Recompute(at(2), prev)
	if len(ch) != 1 || ch[0].New.State != None {
		t.Fatalf("want removal, got %+v", ch)
	}
}

func TestStatusLineAndBadge(t *testing.T) {
	recs := map[string]Record{
		"%1": {State: Blocked}, "%2": {State: Blocked}, "%3": {State: Working},
		"%4": {State: Done}, "%5": {State: Idle},
	}
	s := StatusLine(recs)
	for _, want := range []string{"● 2 blocked", "◐ 1 working", "✓ 1 done"} {
		if !strings.Contains(s, want) {
			t.Errorf("status %q missing %q", s, want)
		}
	}
	if strings.Contains(s, "idle") {
		t.Errorf("idle should not be counted: %q", s)
	}
	if strings.Index(s, "blocked") > strings.Index(s, "working") {
		t.Errorf("blocked should come first: %q", s)
	}
	if StatusLine(map[string]Record{"%1": {State: Idle}}) != "" {
		t.Error("all-idle should render empty")
	}
	if w := Worst([]Record{{State: Working}, {State: Done}, {State: Idle}}); w != Done {
		t.Errorf("worst = %s", w)
	}
	if w := Worst([]Record{{State: Error}, {State: Blocked}}); w != Blocked {
		t.Errorf("worst = %s", w)
	}
}

func TestSanitize(t *testing.T) {
	if got := Sanitize("rm #[fg=red]\x1b\nx", 100); got != "rm ##[fg=red] x" {
		t.Errorf("got %q", got)
	}
	if got := Sanitize(strings.Repeat("a", 10), 4); got != "aaaa…" {
		t.Errorf("got %q", got)
	}
}
