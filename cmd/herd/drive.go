package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/kashym/tmux-agent-herd/internal/api"
	"github.com/kashym/tmux-agent-herd/internal/hooks"
	"github.com/kashym/tmux-agent-herd/internal/state"
)

// resolvePane turns a pane id, tmux target or @herd_name into a pane id.
func resolvePane(g *globals, arg string) (string, error) {
	if arg == "" {
		return "", errors.New("missing pane (use a pane id like %3, a target like main:1.0, or a --name)")
	}
	out, err := g.tc.Run("list-panes", "-a", "-F", "#{pane_id}\x1f#{@herd_name}")
	if err != nil {
		return "", err
	}
	var byName []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		id, name, _ := strings.Cut(line, "\x1f")
		if id == arg {
			return id, nil
		}
		if name != "" && name == arg {
			byName = append(byName, id)
		}
	}
	if len(byName) == 1 {
		return byName[0], nil
	}
	if len(byName) > 1 {
		return "", fmt.Errorf("name %q matches %d panes: %s", arg, len(byName), strings.Join(byName, " "))
	}
	if strings.HasPrefix(arg, "%") {
		return "", fmt.Errorf("no such pane %s", arg)
	}
	id, err := g.tc.Display(arg, "#{pane_id}")
	if err != nil || id == "" {
		return "", fmt.Errorf("no such pane %q", arg)
	}
	return id, nil
}

// checkScope enforces the safety rule: act only on panes in the caller's own
// session unless anySession is set.
func checkScope(g *globals, target string, anySession bool) error {
	if anySession {
		return nil
	}
	caller := os.Getenv("TMUX_PANE")
	if caller == "" {
		return errors.New("not running inside a tmux pane; pass --any-session to target panes explicitly")
	}
	cs, err := g.tc.Display(caller, "#{session_id}")
	if err != nil {
		return err
	}
	ts, err := g.tc.Display(target, "#{session_id}")
	if err != nil {
		return err
	}
	if cs != ts {
		return fmt.Errorf("pane %s is in another tmux session; pass --any-session to allow this", target)
	}
	return nil
}

// paneState asks the daemon for one pane's record ("" if unknown/no daemon).
func paneState(g *globals, id string) (api.Pane, bool) {
	panes, err := listPanes(g, true)
	if err != nil {
		return api.Pane{}, false
	}
	for _, p := range panes {
		if p.PaneID == id {
			return p, true
		}
	}
	return api.Pane{}, false
}

// --- spawn / run ---

func cmdSpawn(g *globals, args []string, stdout io.Writer, isRun bool) error {
	name := "spawn"
	if isRun {
		name = "run"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	right := fs.Bool("right", false, "split to the right (default)")
	down := fs.Bool("down", false, "split below")
	window := fs.Bool("window", false, "open a new window")
	pname := fs.String("name", "", "name for the pane (usable as <pane> later)")
	cwd := fs.String("cwd", "", "working directory (default: caller's)")
	keep := fs.Bool("keep", false, "keep the pane open after the command exits")
	asJSON := fs.Bool("json", false, "JSON output")
	var pos []string
	var err error
	if isRun {
		// herd run [flags] <agent> [agent args...]: flags stop at the agent.
		i := 0
		for i < len(args) && strings.HasPrefix(args[i], "-") && args[i] != "--" {
			if f := fs.Lookup(strings.TrimLeft(args[i], "-")); f != nil {
				if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !bf.IsBoolFlag() {
					i++
				}
			}
			i++
		}
		if i > len(args) {
			return errors.New("flag needs a value")
		}
		if _, err = parseFlags(fs, args[:i]); err != nil {
			return err
		}
		pos = args[i:]
		if len(pos) > 0 && pos[0] == "--" {
			pos = pos[1:]
		}
		if len(pos) == 0 {
			return errors.New("usage: herd run [flags] <agent> [args...]")
		}
	} else if pos, err = parseFlags(fs, args); err != nil {
		return err
	}

	caller := os.Getenv("TMUX_PANE")
	var tmuxArgs []string
	if *window {
		tmuxArgs = []string{"new-window", "-d", "-P", "-F", "#{pane_id}"}
		if *pname != "" {
			tmuxArgs = append(tmuxArgs, "-n", *pname)
		}
		if caller != "" {
			if sess, err := g.tc.Display(caller, "#{session_id}"); err == nil {
				tmuxArgs = append(tmuxArgs, "-t", sess+":")
			}
		}
	} else {
		tmuxArgs = []string{"split-window", "-d", "-P", "-F", "#{pane_id}"}
		if *down {
			tmuxArgs = append(tmuxArgs, "-v")
		} else {
			_ = right
			tmuxArgs = append(tmuxArgs, "-h")
		}
		if caller != "" {
			tmuxArgs = append(tmuxArgs, "-t", caller)
		}
	}
	dir := *cwd
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if dir != "" {
		tmuxArgs = append(tmuxArgs, "-c", dir)
	}
	if !isRun && len(pos) > 0 {
		tmuxArgs = append(tmuxArgs, pos...)
	}
	out, err := g.tc.Run(tmuxArgs...)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(out)
	var post [][]string
	if *pname != "" {
		post = append(post, []string{"set-option", "-p", "-t", id, "@herd_name", *pname},
			[]string{"select-pane", "-t", id, "-T", *pname})
	}
	if *keep {
		post = append(post, []string{"set-option", "-p", "-t", id, "remain-on-exit", "on"})
	}
	if isRun {
		// Start the agent from the pane's shell so the pane (and its output)
		// survives the agent exiting.
		var words []string
		for _, w := range pos {
			words = append(words, hooks.ShellQuote(w))
		}
		post = append(post, []string{"send-keys", "-t", id, "-l", strings.Join(words, " ")},
			[]string{"send-keys", "-t", id, "Enter"})
	}
	for _, c := range post {
		if _, err := g.tc.Run(c...); err != nil {
			return err
		}
	}
	if *asJSON {
		return writeJSON(stdout, map[string]string{"pane_id": id, "name": *pname})
	}
	fmt.Fprintln(stdout, id)
	return nil
}

// --- send ---

func cmdSend(g *globals, args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	enter := fs.Bool("enter", false, "press Enter after the text")
	force := fs.Bool("force", false, "send even if the agent is working")
	any := fs.Bool("any-session", false, "allow panes in other tmux sessions")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return errors.New("usage: herd send <pane> <text> [--enter]")
	}
	id, err := resolvePane(g, pos[0])
	if err != nil {
		return err
	}
	text := strings.Join(pos[1:], " ")
	if text == "" && !*enter {
		return errors.New("nothing to send (give text and/or --enter)")
	}
	if err := checkScope(g, id, *any); err != nil {
		return err
	}
	if id == os.Getenv("TMUX_PANE") {
		return errors.New("refusing to type into your own pane")
	}
	if p, ok := paneState(g, id); ok && p.State == state.Working && !*force {
		return fmt.Errorf("pane %s is working; wait for it (herd wait %s --for done,idle,blocked) or pass --force", id, id)
	}
	// Tell the daemon first, so `wait` ignores states from before this input.
	_, _ = api.Call(g.herdSock, api.Request{Op: api.OpSent, Pane: id}, time.Second)
	if text != "" {
		if strings.Contains(text, "\n") {
			buf := "herd-send-" + strings.TrimPrefix(id, "%")
			if _, err := g.tc.Run("set-buffer", "-b", buf, "--", text); err != nil {
				return err
			}
			if _, err := g.tc.Run("paste-buffer", "-p", "-d", "-b", buf, "-t", id); err != nil {
				return err
			}
		} else if _, err := g.tc.Run("send-keys", "-t", id, "-l", "--", text); err != nil {
			return err
		}
	}
	if *enter {
		if text != "" {
			// TUIs treat text+Enter arriving in one read as a paste; give
			// them a moment so Enter submits.
			time.Sleep(150 * time.Millisecond)
		}
		if _, err := g.tc.Run("send-keys", "-t", id, "Enter"); err != nil {
			return err
		}
	}
	return nil
}

// --- read ---

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]`)

func cmdRead(g *globals, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	n := fs.Int("lines", 50, "number of lines")
	asJSON := fs.Bool("json", false, "JSON output")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: herd read <pane> [--lines N]")
	}
	id, err := resolvePane(g, pos[0])
	if err != nil {
		return err
	}
	text, err := g.tc.Capture(id, *n)
	if err != nil {
		return err
	}
	text = ansiRe.ReplaceAllString(text, "")
	if *asJSON {
		lines := strings.Split(text, "\n")
		for i := range lines {
			lines[i] = strings.TrimRight(lines[i], " ")
		}
		return writeJSON(stdout, map[string]any{"pane_id": id, "lines": lines})
	}
	fmt.Fprintln(stdout, text)
	return nil
}

// --- wait ---

func cmdWait(g *globals, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	forS := fs.String("for", "done,idle", "states to wait for (comma separated)")
	timeout := fs.Float64("timeout", 0, "seconds (0 = no limit)")
	asJSON := fs.Bool("json", false, "JSON output")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return fail(stderr, err)
	}
	if len(pos) != 1 {
		return fail(stderr, errors.New("usage: herd wait <pane> --for done[,idle,blocked] [--timeout S]"))
	}
	id, err := resolvePane(g, pos[0])
	if err != nil {
		return fail(stderr, err)
	}
	callTimeout := time.Duration(0)
	if *timeout > 0 {
		callTimeout = time.Duration(*timeout*float64(time.Second)) + 5*time.Second
	}
	resp, err := api.Call(g.herdSock, api.Request{Op: api.OpWait, Pane: id, For: api.ParseStates(*forS), Timeout: *timeout}, callTimeout)
	if resp.TimedOut {
		if *asJSON {
			_ = writeJSON(stdout, map[string]any{"pane_id": id, "timed_out": true, "pane": resp.Pane})
		} else {
			cur := "unknown"
			if resp.Pane != nil {
				cur = string(resp.Pane.State)
			}
			fmt.Fprintf(stderr, "herd: timed out waiting for %s (currently %s)\n", id, cur)
		}
		return 2
	}
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		_ = writeJSON(stdout, resp.Pane)
	} else if resp.Pane != nil {
		line := resp.Pane.PaneID + " " + string(resp.Pane.State)
		if resp.Pane.Kind != "" {
			line += "/" + resp.Pane.Kind
		}
		if resp.Pane.Msg != "" {
			line += ": " + resp.Pane.Msg
		}
		fmt.Fprintln(stdout, line)
	}
	return 0
}

// --- focus / close ---

func focusPane(g *globals, id, client string) error {
	switch {
	case client != "":
		_, _ = g.tc.Run("switch-client", "-c", client, "-t", id)
	case os.Getenv("TMUX") != "":
		_, _ = g.tc.Run("switch-client", "-t", id)
	}
	if _, err := g.tc.Run("select-window", "-t", id, ";", "select-pane", "-t", id); err != nil {
		return err
	}
	_, _ = api.Call(g.herdSock, api.Request{Op: api.OpFocus, Pane: id}, time.Second)
	return nil
}

func cmdFocus(g *globals, args []string) error {
	fs := flag.NewFlagSet("focus", flag.ContinueOnError)
	client := fs.String("client", "", "tmux client to switch")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: herd focus <pane>")
	}
	id, err := resolvePane(g, pos[0])
	if err != nil {
		return err
	}
	return focusPane(g, id, *client)
}

func cmdClose(g *globals, args []string) error {
	fs := flag.NewFlagSet("close", flag.ContinueOnError)
	any := fs.Bool("any-session", false, "allow panes in other tmux sessions")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: herd close <pane>")
	}
	id, err := resolvePane(g, pos[0])
	if err != nil {
		return err
	}
	if id == os.Getenv("TMUX_PANE") {
		return errors.New("refusing to close your own pane")
	}
	if err := checkScope(g, id, *any); err != nil {
		return err
	}
	_, err = g.tc.Run("kill-pane", "-t", id)
	return err
}
