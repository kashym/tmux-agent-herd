// Package state holds the per-pane agent state table and the priority merge
// between sources (hooks > OSC > screen heuristics > process detection).
package state

import (
	"sort"
	"strings"
	"time"
)

// State is the effective agent state of a pane.
type State string

const (
	None    State = "none"
	Idle    State = "idle"
	Working State = "working"
	Blocked State = "blocked"
	Done    State = "done"
	Error   State = "error"
)

// Valid reports whether s names a known state.
func Valid(s string) bool {
	switch State(s) {
	case None, Idle, Working, Blocked, Done, Error:
		return true
	}
	return false
}

// Severity orders states for "worst state" summaries:
// blocked > error > done > working > idle > none.
func Severity(s State) int {
	switch s {
	case Blocked:
		return 5
	case Error:
		return 4
	case Done:
		return 3
	case Working:
		return 2
	case Idle:
		return 1
	}
	return 0
}

// Source identifies where a report came from. Lower value = higher priority.
type Source int

const (
	SrcHook    Source = 1
	SrcOSC     Source = 2
	SrcScreen  Source = 3
	SrcProcess Source = 4
)

var sourceNames = map[Source]string{SrcHook: "hook", SrcOSC: "osc", SrcScreen: "screen", SrcProcess: "process"}

func (s Source) String() string {
	if n, ok := sourceNames[s]; ok {
		return n
	}
	return "unknown"
}

// ParseSource maps a name back to a Source.
func ParseSource(name string) (Source, bool) {
	for k, v := range sourceNames {
		if v == name {
			return k, true
		}
	}
	return 0, false
}

var allSources = []Source{SrcHook, SrcOSC, SrcScreen, SrcProcess}

// Report is one source's latest view of a pane.
type Report struct {
	State State
	Kind  string
	Msg   string
	At    time.Time
}

// Record is the merged, effective state of one pane.
type Record struct {
	PaneID    string    `json:"pane_id"`
	Agent     string    `json:"agent"`
	State     State     `json:"state"`
	Kind      string    `json:"kind,omitempty"`
	Msg       string    `json:"msg,omitempty"`
	Source    string    `json:"source,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PaneInfo is what tmux tells us about a pane on each poll.
type PaneInfo struct {
	ID       string `json:"pane_id"`
	Command  string `json:"command"`
	Session  string `json:"session"`
	SessID   string `json:"session_id"`
	Window   string `json:"window_id"`
	WinIndex string `json:"window_index"`
	PaneIdx  string `json:"pane_index"`
	Path     string `json:"cwd"`
	Focused  bool   `json:"focused"`
	Pid      int    `json:"-"`
	Activity int64  `json:"-"` // window_activity, unix seconds
}

// Pane is the table's internal per-pane entry.
type Pane struct {
	ID      string
	Agent   string
	Info    PaneInfo
	Reports map[Source]Report
	// FocusAt is the last time the pane gained focus; done reports older than
	// this decay to idle.
	FocusAt time.Time
	// SendAt is the last time `herd send` typed into the pane.
	SendAt time.Time

	wasFocused bool
	lastCmd    string
	screenRaw  State // last raw screen classification (before done derivation)
	eff        Record
}

// Change is one effective-state transition.
type Change struct {
	Old, New Record
	Info     PaneInfo
}

// Table is the state table. It is not safe for concurrent use; the daemon
// serialises access.
type Table struct {
	Agents     map[string]bool
	StaleAfter time.Duration
	panes      map[string]*Pane
}

// New returns an empty table.
func New(agents []string, staleAfter time.Duration) *Table {
	t := &Table{StaleAfter: staleAfter, panes: map[string]*Pane{}}
	t.SetAgents(agents)
	return t
}

// SetAgents replaces the list of commands treated as agents.
func (t *Table) SetAgents(agents []string) {
	t.Agents = map[string]bool{}
	for _, a := range agents {
		if a = strings.TrimSpace(a); a != "" {
			t.Agents[a] = true
		}
	}
}

var shells = map[string]bool{"bash": true, "zsh": true, "fish": true, "sh": true, "dash": true,
	"ksh": true, "tcsh": true, "csh": true, "nu": true, "elvish": true, "xonsh": true, "login": true}

// IsShell reports whether cmd is an interactive shell (agent exited back to it).
func IsShell(cmd string) bool { return shells[strings.TrimPrefix(cmd, "-")] }

func (t *Table) get(id string) *Pane {
	p := t.panes[id]
	if p == nil {
		p = &Pane{ID: id, Reports: map[Source]Report{}, eff: Record{PaneID: id, State: None}}
		t.panes[id] = p
	}
	return p
}

// Pane returns the entry for id, or nil.
func (t *Table) Pane(id string) *Pane { return t.panes[id] }

// Observe applies a tmux snapshot: detects agent panes by command, tracks
// focus transitions, and forgets panes that are gone or whose agent exited.
func (t *Table) Observe(infos []PaneInfo, now time.Time) {
	seen := map[string]bool{}
	for _, in := range infos {
		seen[in.ID] = true
		p := t.panes[in.ID]
		isAgent := t.Agents[in.Command]
		if p == nil && !isAgent {
			continue
		}
		if p == nil {
			p = t.get(in.ID)
		}
		prevCmd := p.lastCmd
		p.lastCmd = in.Command
		p.Info = in

		if isAgent {
			if p.Agent == "" {
				p.Agent = in.Command
			}
			if _, ok := p.Reports[SrcProcess]; !ok {
				p.Reports[SrcProcess] = Report{State: Idle, At: now}
			}
		} else {
			// Agent process exited back to a shell: forget everything.
			exited := IsShell(in.Command) && prevCmd != "" && prevCmd != in.Command
			_, hasHook := p.Reports[SrcHook]
			_, hasOSC := p.Reports[SrcOSC]
			if exited || (!hasHook && !hasOSC) {
				delete(t.panes, in.ID)
				continue
			}
			delete(p.Reports, SrcProcess)
		}

		if in.Focused && !p.wasFocused {
			p.FocusAt = now
		}
		p.wasFocused = in.Focused
	}
	for id := range t.panes {
		if !seen[id] {
			delete(t.panes, id)
		}
	}
}

// Report records an event from src. State None clears that source.
func (t *Table) Report(id string, src Source, agent string, r Report) {
	p := t.get(id)
	if agent != "" {
		p.Agent = agent
	}
	if r.State == None {
		delete(p.Reports, src)
		if src == SrcHook {
			// Session ended: the agent is gone from this pane as far as the
			// hooks are concerned. Drop lower sources too; process detection
			// re-adds the pane if the agent command is still running.
			delete(p.Reports, SrcOSC)
			delete(p.Reports, SrcScreen)
		}
		return
	}
	p.Reports[src] = r
}

// ReportScreen records a raw screen classification. A working→idle
// transition is reported as done (the agent finished a turn); done then
// sticks until the screen changes again or focus decays it.
func (t *Table) ReportScreen(id string, raw Report, now time.Time) {
	p := t.panes[id]
	if p == nil {
		return
	}
	prev := p.screenRaw
	p.screenRaw = raw.State
	cur, have := p.Reports[SrcScreen]
	switch {
	case raw.State == Idle && prev == Working:
		p.Reports[SrcScreen] = Report{State: Done, At: now}
	case raw.State == Idle && have && cur.State == Done:
		// keep done
	case have && cur.State == raw.State && cur.Kind == raw.Kind && cur.Msg == raw.Msg:
		// unchanged; keep original timestamp
	default:
		raw.At = now
		p.Reports[SrcScreen] = raw
	}
}

// Focus marks the pane as just focused (done decays to idle).
func (t *Table) Focus(id string, now time.Time) {
	if p := t.panes[id]; p != nil {
		p.FocusAt = now
	}
}

// Sent marks that input was just typed into the pane.
func (t *Table) Sent(id string, now time.Time) {
	if p := t.panes[id]; p != nil {
		p.SendAt = now
	}
}

// HighFresh reports whether a hook or OSC report is fresh for the pane, in
// which case the screen poller can skip it.
func (t *Table) HighFresh(id string, now time.Time) bool {
	p := t.panes[id]
	if p == nil {
		return false
	}
	for _, s := range []Source{SrcHook, SrcOSC} {
		if r, ok := p.Reports[s]; ok && now.Sub(r.At) <= t.StaleAfter {
			return true
		}
	}
	return false
}

// Effective computes the merged record for a pane.
func (t *Table) Effective(p *Pane, now time.Time) Record {
	rec := Record{PaneID: p.ID, Agent: p.Agent, State: None}
	for _, src := range allSources {
		r, ok := p.Reports[src]
		if !ok {
			continue
		}
		if (src == SrcHook || src == SrcOSC) && t.StaleAfter > 0 && now.Sub(r.At) > t.StaleAfter {
			continue
		}
		rec.State, rec.Kind, rec.Msg, rec.Source, rec.UpdatedAt = r.State, r.Kind, r.Msg, src.String(), r.At
		if r.State == Done && !p.FocusAt.IsZero() && !r.At.After(p.FocusAt) {
			rec.State, rec.Kind, rec.Msg, rec.UpdatedAt = Idle, "", "", p.FocusAt
		}
		break
	}
	if rec.State != Blocked {
		rec.Kind = ""
	}
	if rec.Agent == "" && rec.State != None {
		rec.Agent = "agent"
		if c := p.Info.Command; c != "" && !IsShell(c) {
			rec.Agent = c
		}
	}
	return rec
}

// Recompute merges every pane and returns the transitions since last call.
// Panes removed since the last call are reported with New.State == None.
func (t *Table) Recompute(now time.Time, prev map[string]Record) (map[string]Record, []Change) {
	next := map[string]Record{}
	var changes []Change
	for id, p := range t.panes {
		rec := t.Effective(p, now)
		p.eff = rec
		if rec.State == None {
			continue
		}
		next[id] = rec
		old, ok := prev[id]
		if !ok {
			old = Record{PaneID: id, State: None}
		}
		if old.State != rec.State || old.Kind != rec.Kind || old.Msg != rec.Msg || old.Agent != rec.Agent || old.Source != rec.Source {
			changes = append(changes, Change{Old: old, New: rec, Info: p.Info})
		}
	}
	for id, old := range prev {
		if _, ok := next[id]; !ok {
			var info PaneInfo
			if p := t.panes[id]; p != nil {
				info = p.Info
			}
			changes = append(changes, Change{Old: old, New: Record{PaneID: id, State: None, UpdatedAt: now}, Info: info})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].New.PaneID < changes[j].New.PaneID })
	return next, changes
}

// Panes returns all tracked panes.
func (t *Table) Panes() map[string]*Pane { return t.panes }
