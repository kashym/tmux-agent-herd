package notify

import (
	"testing"
	"time"
)

func TestRateLimitAndCustomCmd(t *testing.T) {
	var calls [][]string
	n := New("echo hi", nil)
	n.Run = func(name string, args []string, env []string) { calls = append(calls, append([]string{name}, args...)) }
	t0 := time.Now()
	ev := Event{Pane: "%1", State: "blocked", Agent: "claude", Msg: "Approve Bash", Where: "main:1.0"}
	if !n.Notify(ev, t0) {
		t.Fatal("first should send")
	}
	if n.Notify(ev, t0.Add(10*time.Second)) {
		t.Fatal("second within 30s should be suppressed")
	}
	if !n.Notify(Event{Pane: "%2", State: "done", Agent: "codex"}, t0.Add(10*time.Second)) {
		t.Fatal("other pane should send")
	}
	if !n.Notify(ev, t0.Add(31*time.Second)) {
		t.Fatal("after 30s should send")
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %d", len(calls))
	}
	c := calls[0]
	if c[0] != "sh" || c[2] != "echo hi" || c[4] != "claude blocked" || c[5] != "main:1.0: Approve Bash" {
		t.Fatalf("bad call %q", c)
	}
}
