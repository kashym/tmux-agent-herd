// Package daemon implements herdd: it owns the state table, merges sources,
// writes pane options, fires notifications and serves the socket API.
package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kashym/tmux-agent-herd/internal/api"
	"github.com/kashym/tmux-agent-herd/internal/notify"
	"github.com/kashym/tmux-agent-herd/internal/poll"
	"github.com/kashym/tmux-agent-herd/internal/state"
	"github.com/kashym/tmux-agent-herd/internal/tmux"
)

// Daemon is one herdd instance bound to one tmux server.
type Daemon struct {
	TC      *tmux.Client
	Sock    string
	Version string
	Log     *log.Logger

	mu       sync.Mutex
	cfg      Config
	tbl      *state.Table
	pats     poll.Set
	notifier *notify.Notifier
	recs     map[string]state.Record
	infos    []state.PaneInfo
	infoByID map[string]state.PaneInfo
	clients  []string
	sendAt   map[string]time.Time
	waiters  map[*waiter]bool
	badges   map[string]string // window id -> badge written
	status   string
	events   *os.File
	ln       net.Listener
	quit     chan struct{}
	cleaned  bool
	failures int
	cfgLine  string
	nextCaps []tmux.CaptureReq
	lastCap  map[string]capInfo
	now      func() time.Time
}

// capInfo remembers when a pane was last captured and its window activity
// timestamp at that moment, so unchanged screens are not captured again.
type capInfo struct {
	act int64
	at  time.Time
}

type waiter struct {
	pane   string
	states map[string]bool
	ch     chan *api.Pane
}

// New prepares a daemon for the given tmux socket.
func New(tmuxSocket, version string) *Daemon {
	return &Daemon{
		TC:      tmux.New(tmuxSocket),
		Sock:    api.SocketPath(tmuxSocket),
		Version: version,
		Log:     log.New(os.Stderr, "herdd: ", log.LstdFlags),
		now:     time.Now,
	}
}

// ErrAlreadyRunning is returned by Run when another daemon holds the lock.
var ErrAlreadyRunning = errors.New("herdd already running for this server")

// Run serves until the tmux server goes away or shutdown is requested.
func (d *Daemon) Run() error {
	lock, err := os.OpenFile(d.Sock+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return ErrAlreadyRunning
	}
	defer os.Remove(d.Sock + ".lock") // runs before the lock is released
	_ = os.Remove(d.Sock)
	ln, err := net.Listen("unix", d.Sock)
	if err != nil {
		return err
	}
	_ = os.Chmod(d.Sock, 0o600)
	defer os.Remove(d.Sock)
	d.ln = ln
	d.quit = make(chan struct{})

	d.cfg = LoadConfig(d.TC)
	d.lastCap = map[string]capInfo{}
	d.tbl = state.New(d.cfg.Agents, d.cfg.StaleAfter)
	d.loadPatterns()
	d.notifier = notify.New(d.cfg.NotifyCmd, d.displayMessage)
	d.recs = map[string]state.Record{}
	d.sendAt = map[string]time.Time{}
	d.waiters = map[*waiter]bool{}
	d.badges = map[string]string{}
	if err := os.MkdirAll(StateDir(), 0o755); err == nil {
		d.events, _ = os.OpenFile(filepath.Join(StateDir(), "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	}
	d.Log.Printf("started %s on %s (tmux %s)", d.Version, d.Sock, d.TC.Socket)

	go d.accept()
	ticker := time.NewTicker(d.cfg.Poll)
	defer ticker.Stop()
	poll := d.cfg.Poll
	d.tick()
	for {
		select {
		case <-d.quit:
			d.shutdownCleanup()
			return nil
		case <-ticker.C:
			if !d.tick() {
				d.Log.Printf("tmux server gone; exiting")
				return nil
			}
			d.mu.Lock()
			if d.cfg.Poll != poll {
				poll = d.cfg.Poll
				ticker.Reset(poll)
			}
			d.mu.Unlock()
		}
	}
}

func (d *Daemon) loadPatterns() {
	set, err := poll.Load(d.cfg.PatternDirs...)
	if err != nil {
		d.Log.Printf("patterns: %v (using built-ins)", err)
		set, _ = poll.Load()
	}
	d.pats = set
}

// tick polls tmux once (one exec). Returns false when the server is gone.
func (d *Daemon) tick() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	snap, err := d.TC.Poll(ConfigFormat(), d.nextCaps)
	if err != nil {
		d.failures++
		return d.failures < 3 || d.TC.Alive()
	}
	d.failures = 0
	if snap.Config != d.cfgLine {
		d.cfgLine = snap.Config
		d.applyConfig(ParseConfigLine(snap.Config))
	}
	d.ingest(snap, now)

	for id, screen := range snap.Captures {
		p := d.tbl.Pane(id)
		if p == nil {
			continue
		}
		if rep, ok := d.pats.Classify(p.Agent, screen); ok {
			d.tbl.ReportScreen(id, rep, now)
		}
		d.lastCap[id] = capInfo{act: d.infoByID[id].Activity, at: now}
	}
	d.flush(now)
	d.nextCaps = d.planCaptures(now)
	return true
}

// planCaptures picks the panes to capture on the next poll: agent panes
// with a pattern file and no fresh hook/OSC source. A pane whose window had
// no output since its last capture is skipped inside tmux.
func (d *Daemon) planCaptures(now time.Time) []tmux.CaptureReq {
	var reqs []tmux.CaptureReq
	for id, p := range d.tbl.Panes() {
		if _, ok := d.pats[p.Agent]; !ok || d.tbl.HighFresh(id, now) {
			continue
		}
		r := tmux.CaptureReq{ID: id}
		if lc, ok := d.lastCap[id]; ok && lc.at.Unix() > lc.act {
			r.SkipIfActivity = lc.act
		}
		reqs = append(reqs, r)
	}
	for id := range d.lastCap {
		if _, ok := d.infoByID[id]; !ok {
			delete(d.lastCap, id)
		}
	}
	return reqs
}

func (d *Daemon) applyConfig(c Config) {
	if !reflect.DeepEqual(c.PatternDirs, d.cfg.PatternDirs) {
		d.cfg.PatternDirs = c.PatternDirs
		d.loadPatterns()
	}
	d.cfg = c
	d.tbl.SetAgents(c.Agents)
	d.tbl.StaleAfter = c.StaleAfter
	d.notifier.Cmd = c.NotifyCmd
}

// observe takes a fresh tmux snapshot into the table (caller holds mu).
func (d *Daemon) observe(now time.Time) bool {
	snap, err := d.TC.Snapshot()
	if err != nil {
		return false
	}
	d.ingest(snap, now)
	return true
}

func (d *Daemon) ingest(snap tmux.Snapshot, now time.Time) {
	if !d.cleaned {
		d.cleanup(snap)
		d.cleaned = true
	}
	for i, in := range snap.Panes {
		if !d.tbl.Agents[in.Command] && tmux.NeedsProcessCheck(in.Command) {
			if a := tmux.AgentInArgs(tmux.ForegroundArgs(in.Pid), d.tbl.Agents); a != "" {
				snap.Panes[i].Command = a
			}
		}
	}
	d.infos = snap.Panes
	d.infoByID = map[string]state.PaneInfo{}
	for _, in := range snap.Panes {
		d.infoByID[in.ID] = in
	}
	d.clients = snap.Clients
	d.tbl.Observe(snap.Panes, now)
	for id := range d.sendAt {
		if _, ok := d.infoByID[id]; !ok {
			delete(d.sendAt, id)
		}
	}
}

var paneOpts = []string{"@herd_state", "@herd_agent", "@herd_msg", "@herd_kind", "@herd_source"}

// cleanup removes options left behind by a previous daemon.
func (d *Daemon) cleanup(snap tmux.Snapshot) {
	var cmds [][]string
	wins := map[string]bool{}
	for _, p := range snap.Panes {
		for _, o := range paneOpts {
			cmds = append(cmds, []string{"set-option", "-p", "-u", "-t", p.ID, o})
		}
		wins[p.Window] = true
	}
	for w := range wins {
		cmds = append(cmds, []string{"set-option", "-w", "-u", "-t", w, "@herd_window_badge"})
	}
	cmds = append(cmds, []string{"set-option", "-g", "@herd_status", ""})
	d.runAll(cmds)
}

// flush recomputes effective state and writes everything that changed
// (caller holds mu).
func (d *Daemon) flush(now time.Time) {
	next, changes := d.tbl.Recompute(now, d.recs)
	d.recs = next
	var cmds [][]string

	for _, ch := range changes {
		id := ch.New.PaneID
		if ch.New.State == state.None {
			if _, alive := d.infoByID[id]; alive {
				for _, o := range paneOpts {
					cmds = append(cmds, []string{"set-option", "-p", "-u", "-t", id, o})
				}
			}
			d.notifier.Forget(id)
		} else {
			r := ch.New
			vals := []string{string(r.State), r.Agent, state.Sanitize(r.Msg, 120), r.Kind, r.Source}
			for i, o := range paneOpts {
				cmds = append(cmds, []string{"set-option", "-p", "-t", id, o, vals[i]})
			}
		}
		d.logEvent(ch, now)
		d.maybeNotify(ch, now)
	}

	// Window badges: worst state per window.
	byWin := map[string][]state.Record{}
	for id, r := range next {
		if in, ok := d.infoByID[id]; ok {
			byWin[in.Window] = append(byWin[in.Window], r)
		}
	}
	for w, recs := range byWin {
		b := state.Badge(state.Worst(recs))
		if d.badges[w] != b {
			cmds = append(cmds, []string{"set-option", "-w", "-t", w, "@herd_window_badge", b})
			d.badges[w] = b
		}
	}
	for w := range d.badges {
		if _, ok := byWin[w]; !ok {
			if d.windowAlive(w) {
				cmds = append(cmds, []string{"set-option", "-w", "-u", "-t", w, "@herd_window_badge"})
			}
			delete(d.badges, w)
		}
	}

	if s := state.StatusLine(next); s != d.status {
		cmds = append(cmds, []string{"set-option", "-g", "@herd_status", s})
		d.status = s
	}
	if len(cmds) > 0 {
		for _, c := range d.clients {
			cmds = append(cmds, []string{"refresh-client", "-S", "-t", c})
		}
		d.runAll(cmds)
	}
	d.checkWaiters()
}

func (d *Daemon) windowAlive(w string) bool {
	for _, in := range d.infos {
		if in.Window == w {
			return true
		}
	}
	return false
}

// runAll runs commands in one exec; if the batch fails (a pane vanished
// mid-way, which aborts the rest of a tmux command list) it retries each
// command on its own.
func (d *Daemon) runAll(cmds [][]string) {
	if len(cmds) == 0 {
		return
	}
	var b tmux.Batch
	for _, c := range cmds {
		b.Add(c...)
	}
	if err := d.TC.RunBatch(&b); err == nil {
		return
	}
	for _, c := range cmds {
		_, _ = d.TC.Run(c...)
	}
}

// Event is one line of events.jsonl.
type Event struct {
	TS     time.Time `json:"ts"`
	Pane   string    `json:"pane"`
	Target string    `json:"target,omitempty"`
	Agent  string    `json:"agent,omitempty"`
	From   string    `json:"from"`
	To     string    `json:"to"`
	Kind   string    `json:"kind,omitempty"`
	Msg    string    `json:"msg,omitempty"`
	Source string    `json:"source,omitempty"`
}

func (d *Daemon) logEvent(ch state.Change, now time.Time) {
	if d.events == nil {
		return
	}
	ev := Event{TS: now, Pane: ch.New.PaneID, Agent: ch.New.Agent, From: string(ch.Old.State), To: string(ch.New.State),
		Kind: ch.New.Kind, Msg: ch.New.Msg, Source: ch.New.Source}
	if ev.Agent == "" {
		ev.Agent = ch.Old.Agent
	}
	if ch.Info.Session != "" {
		ev.Target = fmt.Sprintf("%s:%s.%s", ch.Info.Session, ch.Info.WinIndex, ch.Info.PaneIdx)
	}
	b, _ := json.Marshal(ev)
	_, _ = d.events.Write(append(b, '\n'))
}

func (d *Daemon) maybeNotify(ch state.Change, now time.Time) {
	s := ch.New.State
	if s == ch.Old.State || !d.cfg.Notify[string(s)] || ch.Info.Focused {
		return
	}
	where := fmt.Sprintf("%s:%s.%s", ch.Info.Session, ch.Info.WinIndex, ch.Info.PaneIdx)
	d.notifier.Notify(notify.Event{Pane: ch.New.PaneID, State: string(s), Agent: ch.New.Agent, Msg: ch.New.Msg, Where: where}, now)
}

func (d *Daemon) displayMessage(title, body string) {
	for _, c := range d.clients {
		go func(c string) { _, _ = d.TC.Run("display-message", "-c", c, "herd: "+title+" — "+body) }(c)
	}
}

func (d *Daemon) shutdownCleanup() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for w := range d.waiters {
		close(w.ch)
		delete(d.waiters, w)
	}
	if d.events != nil {
		d.events.Close()
	}
}

// --- socket API ---

func (d *Daemon) accept() {
	for {
		conn, err := d.ln.Accept()
		if err != nil {
			return
		}
		go d.serve(conn)
	}
}

func (d *Daemon) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	enc := json.NewEncoder(conn)
	for {
		line, err := r.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var req api.Request
			var resp api.Response
			if jerr := json.Unmarshal(line, &req); jerr != nil {
				resp = api.Response{Error: "bad request: " + jerr.Error()}
			} else {
				resp = d.handle(req)
			}
			if enc.Encode(resp) != nil {
				return
			}
			if req.Op == api.OpShutdown {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (d *Daemon) handle(req api.Request) api.Response {
	switch req.Op {
	case api.OpPing:
		return api.Response{OK: true, Version: d.Version, Pid: os.Getpid()}
	case api.OpEvent:
		return d.handleEvent(req)
	case api.OpList:
		d.mu.Lock()
		defer d.mu.Unlock()
		if !req.Cached {
			now := d.now()
			d.observe(now)
			d.flush(now)
		}
		return api.Response{OK: true, Panes: d.list(req.All)}
	case api.OpWait:
		return d.handleWait(req)
	case api.OpFocus:
		d.mu.Lock()
		defer d.mu.Unlock()
		now := d.now()
		d.tbl.Focus(req.Pane, now)
		d.flush(now)
		return api.Response{OK: true}
	case api.OpSent:
		d.mu.Lock()
		defer d.mu.Unlock()
		d.sendAt[req.Pane] = d.now()
		return api.Response{OK: true}
	case api.OpShutdown:
		select {
		case <-d.quit:
		default:
			close(d.quit)
			d.ln.Close()
		}
		return api.Response{OK: true}
	}
	return api.Response{Error: "unknown op " + req.Op}
}

func (d *Daemon) handleEvent(req api.Request) api.Response {
	if req.Pane == "" || !strings.HasPrefix(req.Pane, "%") {
		return api.Response{Error: "event needs a pane id like %3"}
	}
	if !state.Valid(req.State) {
		return api.Response{Error: "bad state " + req.State}
	}
	src := state.SrcHook
	if req.Source != "" {
		s, ok := state.ParseSource(req.Source)
		if !ok {
			return api.Response{Error: "bad source " + req.Source}
		}
		src = s
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if src == state.SrcScreen {
		d.tbl.ReportScreen(req.Pane, state.Report{State: state.State(req.State), Kind: req.Kind, Msg: req.Msg}, now)
	} else {
		d.tbl.Report(req.Pane, src, req.Agent, state.Report{State: state.State(req.State), Kind: req.Kind, Msg: req.Msg, At: now})
	}
	if p := d.tbl.Pane(req.Pane); p != nil && p.Info.ID == "" {
		p.Info = d.infoByID[req.Pane]
	}
	if _, known := d.infoByID[req.Pane]; !known {
		// A pane we have not polled yet (just created): refresh first so the
		// next observe does not drop it as unknown.
		d.observe(now)
	}
	d.flush(now)
	return api.Response{OK: true}
}

func (d *Daemon) list(all bool) []api.Pane {
	var out []api.Pane
	for _, in := range d.infos {
		rec, ok := d.recs[in.ID]
		if !ok {
			if !all {
				continue
			}
			rec = state.Record{PaneID: in.ID, State: state.None}
		}
		out = append(out, api.Pane{Record: rec, Command: in.Command, Session: in.Session,
			Window: in.WinIndex, Index: in.PaneIdx, Cwd: in.Path, Focused: in.Focused})
	}
	return out
}

func (d *Daemon) paneRow(id string) *api.Pane {
	in, ok := d.infoByID[id]
	if !ok {
		return nil
	}
	rec, ok := d.recs[id]
	if !ok {
		rec = state.Record{PaneID: id, State: state.None}
	}
	return &api.Pane{Record: rec, Command: in.Command, Session: in.Session,
		Window: in.WinIndex, Index: in.PaneIdx, Cwd: in.Path, Focused: in.Focused}
}

func (d *Daemon) matches(w *waiter) (*api.Pane, bool) {
	row := d.paneRow(w.pane)
	if row == nil {
		return nil, true // pane gone
	}
	if !w.states[string(row.State)] {
		return nil, false
	}
	if sent, ok := d.sendAt[w.pane]; ok && !row.UpdatedAt.After(sent) {
		return nil, false // state predates the last input we typed
	}
	return row, true
}

func (d *Daemon) checkWaiters() {
	for w := range d.waiters {
		if row, ok := d.matches(w); ok {
			w.ch <- row
			delete(d.waiters, w)
		}
	}
}

func (d *Daemon) handleWait(req api.Request) api.Response {
	if len(req.For) == 0 {
		return api.Response{Error: "wait needs at least one state"}
	}
	w := &waiter{pane: req.Pane, states: map[string]bool{}, ch: make(chan *api.Pane, 1)}
	for _, s := range req.For {
		if !state.Valid(s) {
			return api.Response{Error: "bad state " + s}
		}
		w.states[s] = true
	}
	d.mu.Lock()
	now := d.now()
	d.observe(now)
	d.flush(now)
	if _, ok := d.infoByID[req.Pane]; !ok {
		d.mu.Unlock()
		return api.Response{Error: "no such pane " + req.Pane}
	}
	if row, ok := d.matches(w); ok {
		d.mu.Unlock()
		return api.Response{OK: true, Pane: row}
	}
	d.waiters[w] = true
	d.mu.Unlock()

	var timeout <-chan time.Time
	if req.Timeout > 0 {
		t := time.NewTimer(time.Duration(req.Timeout * float64(time.Second)))
		defer t.Stop()
		timeout = t.C
	}
	select {
	case row, ok := <-w.ch:
		if !ok {
			return api.Response{Error: "herdd shutting down"}
		}
		if row == nil {
			return api.Response{Error: "pane " + req.Pane + " closed"}
		}
		return api.Response{OK: true, Pane: row}
	case <-timeout:
		d.mu.Lock()
		delete(d.waiters, w)
		row := d.paneRow(req.Pane)
		d.mu.Unlock()
		return api.Response{OK: false, TimedOut: true, Pane: row, Error: "timed out"}
	}
}
