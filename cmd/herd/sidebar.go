package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/kashym/tmux-agent-herd/internal/api"
	"github.com/kashym/tmux-agent-herd/internal/hooks"
	"github.com/kashym/tmux-agent-herd/internal/state"
)

// cmdSidebar runs the sidebar TUI, or (with --toggle / --ensure) opens or
// closes a sidebar pane in a window.
func cmdSidebar(g *globals, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("sidebar", flag.ContinueOnError)
	toggle := fs.Bool("toggle", false, "open the sidebar in the window, or close it if open")
	ensure := fs.Bool("ensure", false, "open the sidebar in the window unless already open")
	window := fs.String("window", "", "window id (default: current)")
	width := fs.Int("width", 0, "sidebar width in columns (default @herd-sidebar-width or 32)")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	if *toggle || *ensure {
		return sidebarPane(g, *window, *width, *toggle)
	}
	return runSidebar(g, false, "")
}

// sidebarPane finds the sidebar pane in a window and toggles/ensures it.
func sidebarPane(g *globals, window string, width int, toggle bool) error {
	if window == "" {
		w, err := g.tc.Display(os.Getenv("TMUX_PANE"), "#{window_id}")
		if err != nil {
			return err
		}
		window = w
	}
	out, err := g.tc.Run("list-panes", "-t", window, "-F", "#{pane_id}|~|#{@herd_sidebar}")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		id, mark, _ := strings.Cut(line, "|~|")
		if mark == "1" {
			if toggle {
				_, err := g.tc.Run("kill-pane", "-t", id)
				return err
			}
			return nil // already open
		}
	}
	if width <= 0 {
		v, _ := g.tc.Run("show-option", "-gqv", "@herd-sidebar-width")
		width, _ = strconv.Atoi(strings.TrimSpace(v))
		if width < 20 {
			width = 32
		}
	}
	cmd := hooks.ShellQuote(selfExe()) + " -S " + hooks.ShellQuote(g.tmuxSocket) + " sidebar"
	first := strings.SplitN(strings.TrimSpace(out), "|~|", 2)[0]
	id, err := g.tc.Run("split-window", "-hbf", "-d", "-l", strconv.Itoa(width), "-t", first,
		"-P", "-F", "#{pane_id}", cmd)
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	_, err = g.tc.Run("set-option", "-p", "-t", id, "@herd_sidebar", "1", ";", "select-pane", "-t", id, "-T", "herd")
	return err
}

// --- the TUI ---

type sbItem struct {
	session string    // set for a session row
	pane    *api.Pane // set for an agent row
}

type sidebar struct {
	g       *globals
	self    string
	w, h    int
	panes   []api.Pane
	items   []sbItem
	sel     int
	offset  int
	rowAt   map[int]int // screen line -> item index
	mode    string      // "", "send", "kill", "filter"
	popup   bool        // herd mode: a popup that closes after an action
	client  string      // tmux client to act on (popup)
	filter  string
	quit    bool
	mySess  string
	input   []rune
	flash   string
	flashAt time.Time
	err     string
}

const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
	ansiRev   = "\x1b[7m"
	ansiCyan  = "\x1b[36m"
)

func stateColor(s state.State) string {
	if st, ok := state.Style[s]; ok {
		return "\x1b[" + ansiColor[st.Color] + "m"
	}
	return ansiDim
}

func glyph(s state.State) string {
	if st, ok := state.Style[s]; ok {
		return st.Glyph
	}
	return " "
}

func runSidebar(g *globals, popup bool, client string) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return errors.New("herd sidebar needs a terminal (open it with: herd sidebar --toggle)")
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	out := os.Stdout
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l\x1b[?1000h\x1b[?1006h")
	restore := func() {
		fmt.Fprint(out, "\x1b[?1006l\x1b[?1000l\x1b[?25h\x1b[?1049l")
		_ = term.Restore(fd, old)
	}
	defer restore()

	sb := &sidebar{g: g, self: os.Getenv("TMUX_PANE"), rowAt: map[int]int{}, popup: popup, client: client}
	if popup {
		sb.self = ""
		args := []string{"display-message", "-p"}
		if client != "" {
			args = append(args, "-c", client)
		}
		if out, err := g.tc.Run(append(args, "#{session_name}")...); err == nil {
			sb.mySess = strings.TrimSpace(out)
		}
	}
	if sb.self != "" {
		_, _ = g.tc.Run("set-option", "-p", "-t", sb.self, "@herd_sidebar", "1", ";", "select-pane", "-t", sb.self, "-T", "herd")
	}
	sb.resize()

	keys := make(chan []byte, 16)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				close(keys)
				return
			}
			b := make([]byte, n)
			copy(b, buf[:n])
			keys <- b
		}
	}()
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGWINCH, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()

	sb.refresh()
	sb.draw(out)
	for {
		select {
		case b, ok := <-keys:
			if !ok || !sb.handle(b) || sb.quit {
				return nil
			}
		case s := <-sigs:
			if s != syscall.SIGWINCH {
				return nil
			}
			sb.resize()
		case <-tick.C:
			if !sb.refresh() {
				return nil
			}
		}
		sb.draw(out)
	}
}

func (sb *sidebar) resize() {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w < 10 || h < 5 {
		w, h = 32, 24
	}
	sb.w, sb.h = w, h
}

// refresh pulls state from herdd. Returns false when the sidebar should
// exit (it is the last pane left in its window).
func (sb *sidebar) refresh() bool {
	resp, err := api.Call(sb.g.herdSock, api.Request{Op: api.OpList, All: true, Cached: true}, time.Second)
	if err != nil {
		sb.err = "herdd not running"
		_ = ensureDaemon(sb.g)
		return true
	}
	sb.err = ""
	sb.panes = resp.Panes
	var me *api.Pane
	for i := range sb.panes {
		if sb.panes[i].PaneID == sb.self {
			me = &sb.panes[i]
		}
	}
	if me != nil && !sb.popup {
		n := 0
		for _, p := range sb.panes {
			if p.Session == me.Session && p.Window == me.Window {
				n++
			}
		}
		if n <= 1 {
			return false // everything else in the window closed
		}
	}
	sb.buildItems()
	return true
}

func (sb *sidebar) buildItems() {
	var prevKey string
	if sb.sel >= 0 && sb.sel < len(sb.items) {
		prevKey = itemKey(sb.items[sb.sel])
	}
	sb.items = sb.items[:0]
	seen := map[string]bool{}
	var sessions []string
	for _, p := range sb.panes {
		if !seen[p.Session] {
			seen[p.Session] = true
			sessions = append(sessions, p.Session)
		}
	}
	sort.Strings(sessions)
	f := strings.ToLower(sb.filter)
	match := func(parts ...string) bool {
		if f == "" {
			return true
		}
		return strings.Contains(strings.ToLower(strings.Join(parts, " ")), f)
	}
	for _, s := range sessions {
		if match(s) {
			sb.items = append(sb.items, sbItem{session: s})
		}
	}
	var agents []api.Pane
	for _, p := range sb.panes {
		if p.State != state.None && match(p.Agent, p.Target(), p.Msg, string(p.State), p.Cwd) {
			agents = append(agents, p)
		}
	}
	sortForPicker(agents)
	for i := range agents {
		sb.items = append(sb.items, sbItem{pane: &agents[i]})
	}
	sb.sel = min(sb.sel, len(sb.items)-1)
	for i, it := range sb.items {
		if prevKey != "" && itemKey(it) == prevKey {
			sb.sel = i
		}
	}
	if sb.sel < 0 {
		sb.sel = 0
	}
}

func itemKey(it sbItem) string {
	if it.pane != nil {
		return "p" + it.pane.PaneID
	}
	return "s" + it.session
}

// --- input ---

// handle processes one read from the terminal. Returns false to quit.
func (sb *sidebar) handle(b []byte) bool {
	for len(b) > 0 {
		var n int
		var ok bool
		n, ok = sb.handleOne(b)
		if !ok {
			return false
		}
		b = b[n:]
	}
	return true
}

func (sb *sidebar) handleOne(b []byte) (int, bool) {
	// Mouse (SGR): ESC [ < btn ; x ; y (M|m)
	if len(b) >= 3 && b[0] == 0x1b && b[1] == '[' && b[2] == '<' {
		end := 3
		for end < len(b) && b[end] != 'M' && b[end] != 'm' {
			end++
		}
		if end >= len(b) {
			return len(b), true
		}
		f := strings.Split(string(b[3:end]), ";")
		if len(f) == 3 && b[end] == 'M' {
			btn, _ := strconv.Atoi(f[0])
			y, _ := strconv.Atoi(f[2])
			switch btn {
			case 0:
				if i, ok := sb.rowAt[y-1]; ok && sb.mode == "" {
					sb.sel = i
					sb.activate()
				}
			case 64:
				sb.move(-1)
			case 65:
				sb.move(1)
			}
		}
		return end + 1, true
	}
	if sb.mode != "" {
		return sb.handleMode(b)
	}
	if len(b) >= 3 && b[0] == 0x1b && (b[1] == '[' || b[1] == 'O') {
		switch b[2] {
		case 'A':
			sb.move(-1)
		case 'B':
			sb.move(1)
		}
		return 3, true
	}
	switch b[0] {
	case 'q', 0x03:
		return 1, false
	case 0x1b: // lone Esc
		if sb.filter != "" {
			sb.filter = ""
			sb.buildItems()
		} else if sb.popup {
			return 1, false
		}
	case '/':
		sb.mode = "filter"
	case 'p':
		on, err := togglePinned(sb.g)
		switch {
		case err != nil:
			sb.say(err.Error())
		case on:
			sb.say("herd mode pinned: a sidebar in every window")
		default:
			sb.say("herd mode unpinned")
		}
		if sb.popup || !on {
			return 1, false // popup closes; an unpinned sidebar pane is gone anyway
		}
	case 'j':
		sb.move(1)
	case 'k':
		sb.move(-1)
	case 'g':
		sb.sel = 0
	case 'G':
		sb.sel = len(sb.items) - 1
	case 'B':
		sb.jumpTo(state.Blocked)
	case 'D':
		sb.jumpTo(state.Done)
	case '\r', '\n':
		sb.activate()
	case 's':
		if it := sb.current(); it != nil && it.pane != nil {
			sb.mode, sb.input = "send", nil
		}
	case 'x':
		if it := sb.current(); it != nil && it.pane != nil {
			sb.mode = "kill"
		}
	}
	return 1, true
}

func (sb *sidebar) handleMode(b []byte) (int, bool) {
	it := sb.current()
	if sb.mode == "kill" {
		if (b[0] == 'y' || b[0] == 'Y') && it != nil && it.pane != nil {
			if err := cmdClose(sb.g, []string{it.pane.PaneID, "--any-session"}); err != nil {
				sb.say(err.Error())
			}
		}
		sb.mode = ""
		return 1, true
	}
	if sb.mode == "filter" {
		switch {
		case b[0] == 0x1b && len(b) == 1, b[0] == 0x03:
			sb.mode, sb.filter = "", ""
		case b[0] == 0x1b:
			return len(b), true
		case b[0] == '\r' || b[0] == '\n':
			sb.mode = ""
			if len(sb.items) > 0 {
				sb.sel = 0
				for i, it := range sb.items {
					if it.pane != nil { // prefer the first matching agent
						sb.sel = i
						break
					}
				}
			}
		case b[0] == 0x7f || b[0] == 0x08:
			if r := []rune(sb.filter); len(r) > 0 {
				sb.filter = string(r[:len(r)-1])
			}
		default:
			r, size := utf8.DecodeRune(b)
			if r >= 0x20 {
				sb.filter += string(r)
			}
			sb.buildItems()
			return size, true
		}
		sb.buildItems()
		return 1, true
	}
	// send mode: a tiny line editor
	switch {
	case b[0] == 0x1b && len(b) == 1, b[0] == 0x03:
		sb.mode = ""
		return 1, true
	case b[0] == 0x1b:
		return len(b), true // ignore other escape sequences
	case b[0] == '\r' || b[0] == '\n':
		text := strings.TrimSpace(string(sb.input))
		sb.mode = ""
		if text != "" && it != nil && it.pane != nil {
			if err := cmdSend(sb.g, []string{it.pane.PaneID, "--enter", "--force", "--any-session", "--", text}); err != nil {
				sb.say(err.Error())
			} else {
				sb.say("sent to " + it.pane.Agent)
			}
		}
		return 1, true
	case b[0] == 0x7f || b[0] == 0x08:
		if len(sb.input) > 0 {
			sb.input = sb.input[:len(sb.input)-1]
		}
		return 1, true
	}
	r, size := utf8.DecodeRune(b)
	if r >= 0x20 {
		sb.input = append(sb.input, r)
	}
	return size, true
}

func (sb *sidebar) current() *sbItem {
	if sb.sel < 0 || sb.sel >= len(sb.items) {
		return nil
	}
	return &sb.items[sb.sel]
}

func (sb *sidebar) move(d int) {
	sb.sel = max(0, min(len(sb.items)-1, sb.sel+d))
}

func (sb *sidebar) jumpTo(s state.State) {
	for i, it := range sb.items {
		if it.pane != nil && it.pane.State == s {
			sb.sel = i
			sb.activate()
			return
		}
	}
	sb.say("no " + string(s) + " agents")
}

func (sb *sidebar) activate() {
	it := sb.current()
	if it == nil {
		return
	}
	if it.pane != nil {
		if err := focusPane(sb.g, it.pane.PaneID, sb.client); err != nil {
			sb.say(err.Error())
			return
		}
		sb.quit = sb.popup
		return
	}
	args := []string{"switch-client"}
	if sb.client != "" {
		args = append(args, "-c", sb.client)
	}
	if _, err := sb.g.tc.Run(append(args, "-t", it.session)...); err != nil {
		sb.say("can't switch: " + it.session)
		return
	}
	sb.quit = sb.popup
}

func (sb *sidebar) say(msg string) { sb.flash, sb.flashAt = msg, time.Now() }

// --- drawing ---

// fit pads or truncates s (plain text) to exactly w columns.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) > w {
		if w == 1 {
			return "…"
		}
		return string(r[:w-1]) + "…"
	}
	return s + strings.Repeat(" ", w-len(r))
}

// left+right on one line of width w, right-aligned right part.
func spread(left, right string, w int) string {
	lw, rw := utf8.RuneCountInString(left), utf8.RuneCountInString(right)
	if lw+rw+1 > w {
		return fit(left, max(0, w-rw-1)) + " " + right
	}
	return left + strings.Repeat(" ", w-lw-rw) + right
}

type sbLine struct {
	text string // already styled
	item int    // selectable item index or -1
}

func (sb *sidebar) draw(out io.Writer) {
	w := sb.w
	var head, body []sbLine
	add := func(dst *[]sbLine, text string, item int) { *dst = append(*dst, sbLine{text, item}) }

	// Header: title and counts.
	counts := map[state.State]int{}
	for _, p := range sb.panes {
		counts[p.State]++
	}
	var cs []string
	for _, s := range []state.State{state.Blocked, state.Error, state.Working, state.Done} {
		if counts[s] > 0 {
			cs = append(cs, stateColor(s)+glyph(s)+strconv.Itoa(counts[s])+ansiReset)
		}
	}
	right := strings.Join(cs, " ")
	plainRight := stripANSI(right)
	title := " herd"
	pad := max(1, w-utf8.RuneCountInString(title)-utf8.RuneCountInString(plainRight)-1)
	add(&head, ansiBold+title+ansiReset+strings.Repeat(" ", pad)+right+" ", -1)
	add(&head, ansiDim+strings.Repeat("─", w)+ansiReset, -1)

	// Sessions.
	mySession := sb.mySess
	for _, p := range sb.panes {
		if p.PaneID == sb.self {
			mySession = p.Session
		}
	}
	add(&body, ansiDim+fit(" SESSIONS", w)+ansiReset, -1)
	idx := 0
	for ; idx < len(sb.items) && sb.items[idx].pane == nil; idx++ {
		s := sb.items[idx].session
		var recs []state.Record
		n := 0
		for _, p := range sb.panes {
			if p.Session == s && p.State != state.None {
				recs = append(recs, p.Record)
				n++
			}
		}
		worst := state.Worst(recs)
		mark := "  "
		if s == mySession {
			mark = ansiCyan + "▍" + ansiReset + " "
		}
		cnt := ""
		if n > 0 {
			cnt = strconv.Itoa(n)
		}
		line := spread(s, cnt+" ", w-4)
		add(&body, sb.selStyle(idx, mark+stateColor(worst)+glyph(worst)+ansiReset+" "+line), idx)
	}
	add(&body, "", -1)

	// Agents.
	nAgents := len(sb.items) - idx
	add(&body, ansiDim+fit(fmt.Sprintf(" AGENTS %d", nAgents), w)+ansiReset, -1)
	if nAgents == 0 {
		add(&body, ansiDim+fit("   no agents running", w)+ansiReset, -1)
	}
	for ; idx < len(sb.items); idx++ {
		p := sb.items[idx].pane
		mark := "  "
		if p.Focused {
			mark = ansiCyan + "▍" + ansiReset + " "
		}
		age := ""
		if !p.UpdatedAt.IsZero() {
			age = shortDur(time.Since(p.UpdatedAt))
		}
		name := p.Agent
		if p.Kind != "" && p.State == state.Blocked {
			name += " · " + p.Kind
		}
		line1 := spread(name, age+" ", w-4)
		add(&body, sb.selStyle(idx, mark+stateColor(p.State)+glyph(p.State)+ansiReset+" "+line1), idx)
		sub := p.Target()
		if p.Msg != "" {
			sub += " · " + oneLine(p.Msg, 200)
		}
		add(&body, ansiDim+"    "+fit(sub, w-4)+ansiReset, idx)
	}

	// Footer.
	help := " ⏎ go  / find  s send  x kill  p pin"
	if sb.popup {
		help = " ⏎ go  / find  s send  p pin  esc"
	}
	footer := ansiDim + fit(help, w) + ansiReset
	switch {
	case sb.mode == "send":
		prompt := " > " + string(sb.input)
		r := []rune(prompt)
		if len(r) > w-1 {
			prompt = string(r[len(r)-(w-1):])
		}
		footer = ansiBold + fit(prompt+"▏", w) + ansiReset
	case sb.mode == "kill":
		footer = "\x1b[31m" + fit(" kill this pane? y/n", w) + ansiReset
	case sb.mode == "filter" || sb.filter != "":
		footer = ansiBold + fit(" / "+sb.filter+map[bool]string{true: "▏", false: ""}[sb.mode == "filter"], w) + ansiReset
	case sb.err != "":
		footer = "\x1b[31m" + fit(" "+sb.err, w) + ansiReset
	case sb.flash != "" && time.Since(sb.flashAt) < 3*time.Second:
		footer = fit(" "+sb.flash, w)
	}

	// Scroll the body so the selection stays visible.
	avail := sb.h - len(head) - 1
	selLine := -1
	for i, l := range body {
		if l.item == sb.sel {
			if selLine < 0 {
				selLine = i
			}
			if i > selLine+1 {
				break
			}
		}
	}
	if selLine >= 0 {
		if selLine < sb.offset {
			sb.offset = max(0, selLine-1)
		}
		if selLine+2 > sb.offset+avail {
			sb.offset = selLine + 2 - avail
		}
	}
	sb.offset = max(0, min(sb.offset, max(0, len(body)-avail)))

	var b strings.Builder
	b.WriteString("\x1b[H")
	sb.rowAt = map[int]int{}
	y := 0
	emit := func(l sbLine) {
		b.WriteString("\x1b[2K")
		b.WriteString(l.text)
		b.WriteString(ansiReset)
		if l.item >= 0 {
			sb.rowAt[y] = l.item
		}
		y++
		if y < sb.h {
			b.WriteString("\r\n")
		}
	}
	for _, l := range head {
		emit(l)
	}
	for i := sb.offset; i < len(body) && y < sb.h-1; i++ {
		emit(body[i])
	}
	for y < sb.h-1 {
		emit(sbLine{"", -1})
	}
	b.WriteString("\x1b[2K" + footer + ansiReset)
	fmt.Fprint(out, b.String())
}

func (sb *sidebar) selStyle(item int, s string) string {
	if item == sb.sel && sb.mode != "send" {
		// Reverse video for the whole line, keeping glyph colours.
		plain := stripANSI(s)
		pad := max(0, sb.w-utf8.RuneCountInString(plain))
		return ansiRev + strings.ReplaceAll(s, ansiReset, ansiReset+ansiRev) + strings.Repeat(" ", pad) + ansiReset
	}
	return s
}

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }
