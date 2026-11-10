package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kashym/tmux-agent-herd/internal/api"
	"github.com/kashym/tmux-agent-herd/internal/hooks"
	"github.com/kashym/tmux-agent-herd/internal/state"
)

var ansiColor = map[string]string{"red": "31", "magenta": "35", "green": "32", "yellow": "33", "colour244": "90"}

// sortForPicker orders agent panes: blocked first, then error, done,
// working, idle; oldest first within a state.
func sortForPicker(panes []api.Pane) {
	sort.SliceStable(panes, func(i, j int) bool {
		a, b := panes[i], panes[j]
		if sa, sb := state.Severity(a.State), state.Severity(b.State); sa != sb {
			return sa > sb
		}
		return a.UpdatedAt.Before(b.UpdatedAt)
	})
}

func pickerLines(panes []api.Pane) string {
	var b strings.Builder
	for _, p := range panes {
		st := state.Style[p.State]
		label := string(p.State)
		if p.Kind != "" {
			label += "/" + p.Kind
		}
		fmt.Fprintf(&b, "%s\t\x1b[%sm%s %-18s\x1b[0m\t%-8s\t%-14s\t%4s\t%s\n", p.PaneID, ansiColor[st.Color], st.Glyph, label,
			p.Agent, p.Target(), shortDur(time.Since(p.UpdatedAt)), oneLine(p.Msg, 80))
	}
	return b.String()
}

func cmdPick(g *globals, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("pick", flag.ContinueOnError)
	client := fs.String("client", "", "tmux client to switch")
	list := fs.Bool("list", false, "print picker lines and exit")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	panes, err := listPanes(g, false)
	if err != nil {
		return err
	}
	sortForPicker(panes)
	lines := pickerLines(panes)
	if *list {
		fmt.Fprint(stdout, lines)
		return nil
	}
	if len(panes) == 0 {
		fmt.Fprintln(stdout, "No agent panes. (Press any key)")
		waitKey()
		return nil
	}
	self := hooks.ShellQuote(selfExe()) + " -S " + hooks.ShellQuote(g.tmuxSocket)
	fzf, err := exec.LookPath("fzf")
	if err != nil {
		return pickFallback(g, panes, *client, stdout)
	}
	cmd := exec.Command(fzf, "--ansi", "--no-sort", "--layout=reverse", "--delimiter=\t", "--with-nth=2..",
		"--prompt=agent> ", "--header=enter: go · ctrl-x: kill · ctrl-r: send prompt",
		"--preview="+self+" read {1} --lines \"${FZF_PREVIEW_LINES:-40}\"",
		"--preview-window=down,60%,border-top",
		"--bind=ctrl-x:execute-silent("+self+" close {1} --any-session)+reload("+self+" pick --list)",
		"--bind=ctrl-r:execute("+self+" _prompt {1})+reload("+self+" pick --list)",
	)
	cmd.Stdin = strings.NewReader(lines)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && (ee.ExitCode() == 1 || ee.ExitCode() == 130) {
			return nil // no match / cancelled
		}
		return err
	}
	sel := strings.TrimSpace(out.String())
	if sel == "" {
		return nil
	}
	id, _, _ := strings.Cut(sel, "\t")
	return focusPane(g, id, *client)
}

func pickFallback(g *globals, panes []api.Pane, client string, stdout io.Writer) error {
	for i, p := range panes {
		fmt.Fprintf(stdout, "%2d) %-8s %-8s %-14s %s\n", i+1, p.State, p.Agent, p.Target(), oneLine(p.Msg, 50))
	}
	fmt.Fprint(stdout, "\n(fzf not found) pane number: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(panes) {
		return nil
	}
	return focusPane(g, panes[n-1].PaneID, client)
}

func waitKey() {
	b := make([]byte, 1)
	_, _ = os.Stdin.Read(b)
}

// cmdPrompt asks for a line on the terminal and sends it to the pane
// (used by the picker's ctrl-r).
func cmdPrompt(g *globals, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: herd _prompt <pane>")
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer tty.Close()
	fmt.Fprintf(tty, "\x1b[2J\x1b[HSend to %s (empty to cancel):\n> ", args[0])
	line, _ := bufio.NewReader(tty).ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return nil
	}
	return cmdSend(g, []string{args[0], "--enter", "--force", "--any-session", "--", line})
}

func cmdJump(g *globals, args []string) error {
	fs := flag.NewFlagSet("jump", flag.ContinueOnError)
	client := fs.String("client", "", "tmux client to switch")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || (pos[0] != "blocked" && pos[0] != "done") {
		return errors.New("usage: herd jump blocked|done")
	}
	panes, err := listPanes(g, false)
	if err != nil {
		return err
	}
	var cands []api.Pane
	for _, p := range panes {
		if string(p.State) == pos[0] {
			cands = append(cands, p)
		}
	}
	if len(cands) == 0 {
		msg := "herd: no blocked agents"
		if pos[0] == "done" {
			msg = "herd: no unseen finished agents"
		}
		dargs := []string{"display-message"}
		if *client != "" {
			dargs = append(dargs, "-c", *client)
		}
		_, _ = g.tc.Run(append(dargs, msg)...)
		return nil
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].UpdatedAt.Before(cands[j].UpdatedAt) })
	return focusPane(g, cands[0].PaneID, *client)
}
