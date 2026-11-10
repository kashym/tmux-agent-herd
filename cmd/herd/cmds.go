package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/kashym/tmux-agent-herd/internal/api"
	"github.com/kashym/tmux-agent-herd/internal/daemon"
	"github.com/kashym/tmux-agent-herd/internal/harness"
	"github.com/kashym/tmux-agent-herd/internal/hooks"
	"github.com/kashym/tmux-agent-herd/internal/state"
)

// --- daemon ---

func cmdDaemon(g *globals, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	ensure := fs.Bool("ensure", false, "start in the background unless already running")
	stop := fs.Bool("stop", false, "stop the running daemon")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	if *stop {
		_, err := api.Call(g.herdSock, api.Request{Op: api.OpShutdown}, 3*time.Second)
		return err
	}
	if *ensure {
		return ensureDaemon(g)
	}
	d := daemon.New(g.tmuxSocket, version)
	err := d.Run()
	if errors.Is(err, daemon.ErrAlreadyRunning) {
		fmt.Fprintln(stdout, "herdd already running")
		return nil
	}
	return err
}

func ensureDaemon(g *globals) error {
	if resp, err := api.Call(g.herdSock, api.Request{Op: api.OpPing}, time.Second); err == nil {
		if resp.Version == version {
			return nil
		}
		// Plugin was updated: replace the old daemon.
		_, _ = api.Call(g.herdSock, api.Request{Op: api.OpShutdown}, 2*time.Second)
		for i := 0; i < 20; i++ {
			if _, err := api.Call(g.herdSock, api.Request{Op: api.OpPing}, 200*time.Millisecond); err != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !g.tc.Alive() {
		return fmt.Errorf("no tmux server on %s", g.tmuxSocket)
	}
	_ = os.MkdirAll(daemon.StateDir(), 0o755)
	logPath := filepath.Join(daemon.StateDir(), "herdd.log")
	if st, err := os.Stat(logPath); err == nil && st.Size() > 1<<20 {
		_ = os.Rename(logPath, logPath+".1")
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(selfExe(), "-S", g.tmuxSocket, "daemon")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	for i := 0; i < 30; i++ {
		if _, err := api.Call(g.herdSock, api.Request{Op: api.OpPing}, 200*time.Millisecond); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("herdd did not start; see %s", logPath)
}

// --- ls ---

func listPanes(g *globals, all bool) ([]api.Pane, error) {
	resp, err := api.Call(g.herdSock, api.Request{Op: api.OpList, All: all}, 5*time.Second)
	if err != nil {
		return nil, err
	}
	return resp.Panes, nil
}

func cmdLs(g *globals, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	agents := fs.Bool("agents", false, "only agent panes")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	panes, err := listPanes(g, !*agents)
	if err != nil {
		return err
	}
	if *asJSON {
		if panes == nil {
			panes = []api.Pane{}
		}
		return writeJSON(stdout, panes)
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PANE\tTARGET\tAGENT\tSTATE\tSOURCE\tAGE\tCOMMAND\tCWD\tMSG")
	for _, p := range panes {
		st := string(p.State)
		if p.Kind != "" {
			st += "/" + p.Kind
		}
		age := ""
		if !p.UpdatedAt.IsZero() && p.State != state.None {
			age = shortDur(time.Since(p.UpdatedAt))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", p.PaneID, p.Target(), dash(p.Agent), st,
			dash(p.Source), dash(age), p.Command, tildify(p.Cwd), oneLine(p.Msg, 60))
	}
	return tw.Flush()
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func shortDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func tildify(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && strings.HasPrefix(p, h) {
		return "~" + p[len(h):]
	}
	return p
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// --- hook / event ---

func cmdHook(g *globals, args []string) error {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	agent := fs.String("agent", "", "agent name (default: the pane's detected agent)")
	pane := fs.String("pane", os.Getenv("TMUX_PANE"), "pane id")
	msg := fs.String("msg", "", "message (overrides the payload's)")
	kind := fs.String("kind", "", "blocked kind: permission, question, auth")
	source := fs.String("source", "hook", "hook, or screen for completion-only signals (aider)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if *pane == "" {
		return nil // not in tmux: nothing to do
	}
	arg := ""
	if len(pos) > 0 {
		arg = pos[0]
	}
	payload := hooks.ParsePayload(readStdinQuick(time.Second))
	ev, ok := hooks.Map(arg, payload)
	if !ok {
		return nil
	}
	if *msg != "" {
		ev.Msg = *msg
	}
	if *kind != "" && ev.State == state.Blocked {
		ev.Kind = *kind
	}
	_, err = api.Call(g.herdSock, api.Request{Op: api.OpEvent, Pane: *pane, Agent: *agent,
		State: string(ev.State), Kind: ev.Kind, Msg: ev.Msg, Source: *source}, 2*time.Second)
	return err
}

// readStdinQuick reads stdin if it is a pipe/file, giving up after d.
func readStdinQuick(d time.Duration) []byte {
	st, err := os.Stdin.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice != 0 {
		return nil
	}
	ch := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		ch <- b
	}()
	select {
	case b := <-ch:
		return b
	case <-time.After(d):
		return nil
	}
}

func cmdEvent(g *globals, args []string) error {
	fs := flag.NewFlagSet("event", flag.ContinueOnError)
	pane := fs.String("pane", os.Getenv("TMUX_PANE"), "pane id")
	st := fs.String("state", "", "state")
	src := fs.String("source", "hook", "source: hook or screen")
	agent := fs.String("agent", "", "agent name")
	kind := fs.String("kind", "", "blocked kind: permission, question, auth")
	msg := fs.String("msg", "", "message")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	id, err := resolvePane(g, *pane)
	if err != nil {
		return err
	}
	_, err = api.Call(g.herdSock, api.Request{Op: api.OpEvent, Pane: id, State: *st, Source: *src,
		Agent: *agent, Kind: *kind, Msg: *msg}, 3*time.Second)
	return err
}

func cmdFocusEvent(g *globals, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: herd _focus <pane-id>")
	}
	_, err := api.Call(g.herdSock, api.Request{Op: api.OpFocus, Pane: args[0]}, 2*time.Second)
	if errors.Is(err, api.ErrNotRunning) {
		return nil
	}
	return err
}

// --- log ---

func cmdLog(g *globals, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	n := fs.Int("lines", 30, "number of events")
	asJSON := fs.Bool("json", false, "JSON lines output")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	want := ""
	if len(pos) > 0 {
		if want, err = resolvePane(g, pos[0]); err != nil {
			want = pos[0] // pane may be gone; still filter by the given id
		}
	}
	f, err := os.Open(filepath.Join(daemon.StateDir(), "events.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	var evs []daemon.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var ev daemon.Event
		if json.Unmarshal(sc.Bytes(), &ev) != nil || (want != "" && ev.Pane != want) {
			continue
		}
		evs = append(evs, ev)
		if len(evs) > *n*4 {
			evs = evs[len(evs)-*n:]
		}
	}
	if len(evs) > *n {
		evs = evs[len(evs)-*n:]
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		for _, ev := range evs {
			_ = enc.Encode(ev)
		}
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, ev := range evs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s → %s\tsource=%s\t%s\n", ev.TS.Local().Format("15:04:05"), ev.Pane,
			dash(ev.Target), dash(ev.Agent), ev.From, ev.To, dash(ev.Source), oneLine(ev.Msg, 70))
	}
	return tw.Flush()
}

// --- setup / install-skill ---

func hookPath() string { return filepath.Join(pluginDir(), "scripts", "herd-hook") }

func cmdSetup(g *globals, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "apply without asking")
	list := fs.Bool("list", false, "show supported harnesses and their status")
	remove := fs.Bool("remove", false, "take herd back out of the harness config")
	hp := fs.String("hook-path", hookPath(), "path to herd-hook")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	usage := fmt.Errorf("usage: herd setup <harness...|all> [--yes]   (harnesses: %s)\n       herd setup --list",
		strings.Join(harness.Names(), ", "))
	if *list {
		return setupList(stdout, *hp)
	}
	if len(pos) == 0 {
		return usage
	}
	if _, err := os.Stat(*hp); err != nil && !*remove {
		return fmt.Errorf("herd-hook not found at %s", *hp)
	}
	var targets []*harness.Harness
	if len(pos) == 1 && pos[0] == "all" {
		for _, h := range harness.All() {
			if *remove || h.Installed() {
				targets = append(targets, h)
			}
		}
		if len(targets) == 0 {
			fmt.Fprintln(stdout, "No supported agent harness found on this machine.")
			return nil
		}
	} else {
		for _, name := range pos {
			h, err := harness.Get(name)
			if err != nil {
				return err
			}
			targets = append(targets, h)
		}
	}
	var failed []string
	for i, h := range targets {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		run := setupOne
		if *remove {
			run = removeOne
		}
		if err := run(h, *hp, *yes, stdout); err != nil {
			fmt.Fprintf(stdout, "%s: %v\n", h.Title, err)
			failed = append(failed, h.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("setup failed for: %s", strings.Join(failed, ", "))
	}
	return nil
}

func setupList(stdout io.Writer, hp string) error {
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HARNESS\tINSTALLED\tHERD SET UP\tVIA\tFILE")
	for _, s := range harness.Statuses(pluginDir(), hp) {
		conf := "no"
		if s.Configured {
			conf = "yes"
		} else if s.Err != nil {
			conf = "error"
		}
		inst := "no"
		if s.Installed {
			inst = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Harness.Name, inst, conf, s.Harness.Hooks, tildify(s.Harness.Target()))
	}
	return tw.Flush()
}

func setupOne(h *harness.Harness, hp string, yes bool, stdout io.Writer) error {
	path := h.Target()
	old, next, err := h.Plan(pluginDir(), hp)
	if err != nil {
		return err
	}
	if old != nil && bytes.Equal(old, next) {
		fmt.Fprintf(stdout, "%s: herd is already set up in %s\n", h.Title, tildify(path))
		return nil
	}
	fmt.Fprintf(stdout, "%s: proposed change to %s:\n\n", h.Title, tildify(path))
	showDiff(stdout, old, next)
	if !yes {
		ok, err := confirm("\nApply this change? [y/N] ")
		if err != nil {
			return fmt.Errorf("cannot ask for confirmation (%v); re-run with --yes to apply", err)
		}
		if !ok {
			fmt.Fprintln(stdout, "Not changed.")
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if old != nil {
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
		backup := path + ".herd-backup-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(backup, old, mode); err != nil {
			return fmt.Errorf("backup failed, not changing anything: %w", err)
		}
		fmt.Fprintln(stdout, "Backup:", tildify(backup))
	}
	tmp := path + ".herd-tmp"
	if err := os.WriteFile(tmp, next, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Updated %s. %s\n", tildify(path), h.Note)
	return nil
}

func removeOne(h *harness.Harness, _ string, yes bool, stdout io.Writer) error {
	path := h.Target()
	old, next, del, err := h.PlanRemove()
	if err != nil {
		return err
	}
	if old == nil {
		fmt.Fprintf(stdout, "%s: herd is not set up in %s\n", h.Title, tildify(path))
		return nil
	}
	if del {
		fmt.Fprintf(stdout, "%s: will delete %s (it only holds herd's config)\n", h.Title, tildify(path))
	} else {
		fmt.Fprintf(stdout, "%s: proposed change to %s:\n\n", h.Title, tildify(path))
		showDiff(stdout, old, next)
	}
	if !yes {
		ok, err := confirm("\nApply? [y/N] ")
		if err != nil {
			return fmt.Errorf("cannot ask for confirmation (%v); re-run with --yes", err)
		}
		if !ok {
			fmt.Fprintln(stdout, "Not changed.")
			return nil
		}
	}
	if del {
		if err := os.Remove(path); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Removed", tildify(path))
		return nil
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	backup := path + ".herd-backup-" + time.Now().Format("20060102-150405")
	if err := os.WriteFile(backup, old, mode); err != nil {
		return fmt.Errorf("backup failed, not changing anything: %w", err)
	}
	if err := os.WriteFile(path, next, mode); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Updated %s (backup: %s)\n", tildify(path), tildify(backup))
	return nil
}

func showDiff(w io.Writer, a, b []byte) {
	if _, err := exec.LookPath("diff"); err == nil {
		dir, err := os.MkdirTemp("", "herd-diff")
		if err == nil {
			defer os.RemoveAll(dir)
			pa, pb := filepath.Join(dir, "old"), filepath.Join(dir, "new")
			_ = os.WriteFile(pa, a, 0o600)
			_ = os.WriteFile(pb, b, 0o600)
			out, _ := exec.Command("diff", "-u", "--label", "current", "--label", "with herd", pa, pb).Output()
			if len(out) > 0 {
				w.Write(out)
				return
			}
		}
	}
	w.Write(b)
}

func confirm(prompt string) (bool, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	defer tty.Close()
	fmt.Fprint(tty, prompt)
	line, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil && line == "" {
		return false, err
	}
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes", nil
}

// cmdInstallSkill copies skills/herd/SKILL.md into each harness's personal
// skills directory (all installed harnesses by default).
func cmdInstallSkill(g *globals, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("install-skill", flag.ContinueOnError)
	dir := fs.String("dir", "", "install into this skills directory instead")
	remove := fs.Bool("remove", false, "remove the herd skill instead")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(pluginDir(), "skills", "herd", "SKILL.md"))
	if err != nil {
		return fmt.Errorf("skill not found: %w", err)
	}
	var dirs []string
	switch {
	case *dir != "":
		dirs = []string{*dir}
	case len(pos) == 0 || (len(pos) == 1 && pos[0] == "all"):
		for _, h := range harness.All() {
			if d := h.SkillDir(); d != "" && (h.Installed() || *remove) {
				dirs = append(dirs, d)
			}
		}
		if len(dirs) == 0 {
			return errors.New("no harness with skill support found; pass a harness name or --dir")
		}
	default:
		for _, name := range pos {
			h, err := harness.Get(name)
			if err != nil {
				return err
			}
			if h.SkillDir() == "" {
				return fmt.Errorf("%s has no skills support", h.Title)
			}
			dirs = append(dirs, h.SkillDir())
		}
	}
	for _, d := range dirs {
		target := filepath.Join(d, "herd", "SKILL.md")
		if *remove {
			if old, err := os.ReadFile(target); err == nil && strings.Contains(string(old), "name: herd") {
				_ = os.Remove(target)
				_ = os.Remove(filepath.Dir(target)) // only if empty
				fmt.Fprintln(stdout, "Removed:", tildify(target))
			}
			continue
		}
		if old, err := os.ReadFile(target); err == nil && bytes.Equal(old, data) {
			fmt.Fprintln(stdout, "Up to date:", tildify(target))
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Installed:", tildify(target))
	}
	return nil
}
