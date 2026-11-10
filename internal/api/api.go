// Package api defines the herdd socket protocol (newline-delimited JSON, one
// request per line, one response per request) and a client for it.
package api

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kashym/tmux-agent-herd/internal/state"
)

// Request ops.
const (
	OpPing     = "ping"
	OpEvent    = "event"    // a source reports a state for a pane
	OpList     = "ls"       // list panes with state
	OpWait     = "wait"     // block until a pane reaches a state
	OpFocus    = "focus"    // pane gained focus (done decays)
	OpSent     = "sent"     // input was typed into a pane
	OpShutdown = "shutdown" // stop the daemon
)

// Request is one client request.
type Request struct {
	Op      string   `json:"op"`
	Pane    string   `json:"pane,omitempty"`
	Agent   string   `json:"agent,omitempty"`
	State   string   `json:"state,omitempty"`
	Kind    string   `json:"kind,omitempty"`
	Msg     string   `json:"msg,omitempty"`
	Source  string   `json:"source,omitempty"`
	For     []string `json:"for,omitempty"`
	Timeout float64  `json:"timeout,omitempty"` // seconds, 0 = forever
	All     bool     `json:"all,omitempty"`     // ls: include non-agent panes
	Cached  bool     `json:"cached,omitempty"`  // ls: last poll's view, no fresh tmux scan
}

// Pane is one row of `ls`.
type Pane struct {
	state.Record
	Command string `json:"command"`
	Session string `json:"session"`
	Window  string `json:"window_index"`
	Index   string `json:"pane_index"`
	Cwd     string `json:"cwd"`
	Focused bool   `json:"focused"`
}

// Target renders session:window.pane.
func (p Pane) Target() string { return fmt.Sprintf("%s:%s.%s", p.Session, p.Window, p.Index) }

// Response is the daemon's reply.
type Response struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Version  string `json:"version,omitempty"`
	Pid      int    `json:"pid,omitempty"`
	Panes    []Pane `json:"panes,omitempty"`
	Pane     *Pane  `json:"pane,omitempty"`
	TimedOut bool   `json:"timed_out,omitempty"`
}

// SocketPath returns the herdd socket for a tmux server socket: a file named
// herd-<socket name>.sock next to the tmux socket (which lives in
// $TMUX_TMPDIR/tmux-<uid>/, a private per-user directory).
// $HERD_SOCKET overrides it.
func SocketPath(tmuxSocket string) string {
	if v := os.Getenv("HERD_SOCKET"); v != "" {
		return v
	}
	return filepath.Join(filepath.Dir(tmuxSocket), "herd-"+filepath.Base(tmuxSocket)+".sock")
}

// ErrNotRunning means no daemon answered on the socket.
var ErrNotRunning = errors.New("herdd is not running (start it with: herd daemon --ensure)")

// Call sends one request and reads one response. timeout bounds dialing and
// the whole exchange (0 = no limit after dialing).
func Call(sock string, req Request, timeout time.Duration) (Response, error) {
	dial := timeout
	if dial == 0 || dial > 2*time.Second {
		dial = 2 * time.Second
	}
	conn, err := net.DialTimeout("unix", sock, dial)
	if err != nil {
		return Response{}, ErrNotRunning
	}
	defer conn.Close()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	b, _ := json.Marshal(req)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return Response{}, err
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return Response{}, fmt.Errorf("reading herdd reply: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, err
	}
	if !resp.OK && resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

// ParseStates splits "done,idle" or "done/idle" into a list.
func ParseStates(s string) []string {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '/' || r == ' ' })
	return f
}
