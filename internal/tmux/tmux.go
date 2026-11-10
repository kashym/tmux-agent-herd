// Package tmux is a thin wrapper over the tmux CLI bound to one server socket.
package tmux

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kashym/tmux-agent-herd/internal/state"
)

// Client runs tmux commands against one server.
type Client struct {
	Socket  string // absolute socket path (-S)
	Bin     string // tmux binary, default "tmux"
	Timeout time.Duration
}

// New returns a client for socket.
func New(socket string) *Client {
	return &Client{Socket: socket, Bin: "tmux", Timeout: 5 * time.Second}
}

// SocketFromEnv returns the server socket of the tmux we are running inside,
// taken from $TMUX ("socket,pid,session").
func SocketFromEnv() string {
	v := os.Getenv("TMUX")
	if v == "" {
		return ""
	}
	if i := strings.IndexByte(v, ','); i >= 0 {
		return v[:i]
	}
	return v
}

// SocketForName returns the socket path tmux uses for `-L name`.
func SocketForName(name string) string {
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	return filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()), name)
}

// Run executes one tmux command (args may contain ";" separators for
// multiple commands) and returns stdout.
func (c *Client) Run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()
	full := append([]string{"-S", c.Socket}, args...)
	cmd := exec.CommandContext(ctx, c.Bin, full...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("tmux %s: %s", strings.Join(args, " "), msg)
	}
	return out.String(), nil
}

// Batch collects several tmux commands to run in one exec.
type Batch struct{ args []string }

// Add appends one command.
func (b *Batch) Add(args ...string) {
	if len(b.args) > 0 {
		b.args = append(b.args, ";")
	}
	b.args = append(b.args, args...)
}

// Len is the number of argument words queued.
func (b *Batch) Len() int { return len(b.args) }

// Run executes the batch.
func (c *Client) RunBatch(b *Batch) error {
	if b.Len() == 0 {
		return nil
	}
	_, err := c.Run(b.args...)
	return err
}

// Field and record markers are printable on purpose: tmux escapes control
// characters in format output under some locales (e.g. LC_ALL=C turns
// \x1f into "_").
const (
	sep       = "|~|"
	capPrefix = "@@herd-capture:"
	capSuffix = "@@"
)

var paneFormat = strings.Join([]string{
	"P", "#{pane_id}", "#{pane_current_command}", "#{session_name}", "#{session_id}",
	"#{window_id}", "#{window_index}", "#{pane_index}", "#{pane_current_path}",
	"#{pane_active}", "#{window_active}", "#{session_attached}", "#{pane_pid}", "#{window_activity}",
}, sep)

// Snapshot is one poll of the server.
type Snapshot struct {
	Panes    []state.PaneInfo
	Clients  []string
	Config   string            // expanded configFormat (Poll only)
	Captures map[string]string // pane id -> visible screen (Poll only)
}

// CaptureReq asks Poll to capture a pane. If SkipIfActivity is non-zero the
// capture is skipped (inside tmux, no extra exec) when the pane's window
// activity timestamp still equals it.
type CaptureReq struct {
	ID             string
	SkipIfActivity int64
}

// Snapshot lists every pane and client in one exec.
func (c *Client) Snapshot() (Snapshot, error) {
	return c.Poll("", nil)
}

// Poll lists panes and clients, expands configFormat, and captures the
// requested panes, all in a single tmux exec.
func (c *Client) Poll(configFormat string, caps []CaptureReq) (Snapshot, error) {
	var b Batch
	b.Add("list-panes", "-a", "-F", paneFormat)
	b.Add("list-clients", "-F", "C"+sep+"#{client_name}")
	if configFormat != "" {
		b.Add("display-message", "-p", "O"+sep+configFormat)
	}
	for _, r := range caps {
		capture := "display-message -p -t " + r.ID + " '" + capPrefix + "#{pane_id}" + capSuffix + "' ; capture-pane -p -J -t " + r.ID
		if r.SkipIfActivity != 0 {
			b.Add("if-shell", "-F", "-t", r.ID, "#{!=:#{window_activity},"+strconv.FormatInt(r.SkipIfActivity, 10)+"}", capture)
		} else {
			b.Add("if-shell", "-F", "-t", r.ID, "1", capture)
		}
	}
	out, err := c.Run(b.args...)
	if err != nil && !strings.HasPrefix(out, "P"+sep) {
		return Snapshot{}, err
	}
	return ParseSnapshot(out), nil
}

// ParseSnapshot parses Poll output.
func ParseSnapshot(out string) Snapshot {
	s := Snapshot{Captures: map[string]string{}}
	cur := "" // pane id whose capture we are reading
	var buf []string
	flush := func() {
		if cur != "" {
			s.Captures[cur] = strings.Join(buf, "\n")
		}
		buf = nil
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, capPrefix) && strings.HasSuffix(line, capSuffix) {
			flush()
			cur = strings.TrimSuffix(strings.TrimPrefix(line, capPrefix), capSuffix)
			continue
		}
		if cur != "" {
			buf = append(buf, line)
			continue
		}
		f := strings.Split(line, sep)
		switch {
		case len(f) == 14 && f[0] == "P":
			attached, _ := strconv.Atoi(f[11])
			pid, _ := strconv.Atoi(f[12])
			act, _ := strconv.ParseInt(f[13], 10, 64)
			s.Panes = append(s.Panes, state.PaneInfo{
				ID: f[1], Command: f[2], Session: f[3], SessID: f[4], Window: f[5],
				WinIndex: f[6], PaneIdx: f[7], Path: f[8],
				Focused:  f[9] == "1" && f[10] == "1" && attached > 0,
				Pid:      pid,
				Activity: act,
			})
		case len(f) == 2 && f[0] == "C":
			s.Clients = append(s.Clients, f[1])
		case len(f) > 0 && f[0] == "O":
			s.Config = strings.TrimPrefix(line, "O"+sep)
		}
	}
	flush()
	return s
}

// Capture returns the last n lines of a pane's history+screen, joined.
func (c *Client) Capture(id string, lines int) (string, error) {
	args := []string{"capture-pane", "-p", "-J", "-t", id}
	if lines > 0 {
		args = append(args, "-S", "-"+strconv.Itoa(lines+200))
	}
	out, err := c.Run(args...)
	if err != nil {
		return "", err
	}
	all := strings.Split(strings.TrimRight(out, "\n "), "\n")
	if lines > 0 && len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n"), nil
}

// Display expands a format, optionally against a target.
func (c *Client) Display(target, format string) (string, error) {
	args := []string{"display-message", "-p"}
	if target != "" {
		args = append(args, "-t", target)
	}
	out, err := c.Run(append(args, format)...)
	return strings.TrimRight(out, "\n"), err
}

// Alive reports whether the server responds.
func (c *Client) Alive() bool {
	_, err := c.Run("list-sessions", "-F", "x")
	return err == nil
}
