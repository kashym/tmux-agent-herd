// Package notify fires desktop notifications for state transitions, rate
// limited per pane.
package notify

import (
	"context"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// Notifier sends notifications.
type Notifier struct {
	// Cmd is a custom shell command (@herd-notify-cmd). It runs via
	// `sh -c "$Cmd" herd <title> <body>`, so $1 is the title and $2 the body.
	// HERD_PANE, HERD_STATE, HERD_AGENT and HERD_MSG are also set.
	Cmd      string
	Interval time.Duration
	// Fallback is called when no command is available (display-message).
	Fallback func(title, body string)
	// Run executes commands; tests replace it.
	Run func(name string, args []string, env []string)

	mu   sync.Mutex
	last map[string]time.Time
}

// New returns a notifier with the default 30 s per-pane rate limit.
func New(cmd string, fallback func(title, body string)) *Notifier {
	return &Notifier{Cmd: cmd, Interval: 30 * time.Second, Fallback: fallback, Run: runAsync, last: map[string]time.Time{}}
}

// Event describes one notification.
type Event struct {
	Pane, State, Agent, Msg, Where string
}

// Notify sends ev unless the pane was notified within Interval. Returns
// whether a notification was sent.
func (n *Notifier) Notify(ev Event, now time.Time) bool {
	n.mu.Lock()
	if t, ok := n.last[ev.Pane]; ok && now.Sub(t) < n.Interval {
		n.mu.Unlock()
		return false
	}
	n.last[ev.Pane] = now
	n.mu.Unlock()

	title := ev.Agent + " " + ev.State
	body := ev.Where
	if ev.Msg != "" {
		body += ": " + ev.Msg
	}
	env := []string{"HERD_PANE=" + ev.Pane, "HERD_STATE=" + ev.State, "HERD_AGENT=" + ev.Agent, "HERD_MSG=" + ev.Msg}

	switch {
	case n.Cmd != "":
		n.Run("sh", []string{"-c", n.Cmd, "herd", title, body}, env)
	case runtime.GOOS == "darwin" && has("osascript"):
		n.Run("osascript", []string{"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", title, body}, env)
	case has("notify-send"):
		n.Run("notify-send", []string{"-a", "tmux-agent-herd", title, body}, env)
	case n.Fallback != nil:
		n.Fallback(title, body)
	}
	return true
}

// Forget drops rate-limit state for a pane that went away.
func (n *Notifier) Forget(pane string) {
	n.mu.Lock()
	delete(n.last, pane)
	n.mu.Unlock()
}

func has(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

func runAsync(name string, args []string, env []string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = append(cmd.Environ(), env...)
		_ = cmd.Run()
	}()
}
