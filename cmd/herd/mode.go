package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/kashym/tmux-agent-herd/internal/hooks"
)

// cmdMode is "herd mode": the dashboard as a popup docked on the left
// (opened by the plugin's one key), or --pin to keep a sidebar in every
// window.
func cmdMode(g *globals, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("mode", flag.ContinueOnError)
	client := fs.String("client", "", "tmux client the popup belongs to")
	pin := fs.String("pin", "", "on, off or toggle: keep a sidebar in every window")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	switch *pin {
	case "":
		return runSidebar(g, true, *client)
	case "on", "off":
		return setPinned(g, *pin == "on")
	case "toggle":
		on, err := togglePinned(g)
		if err == nil {
			fmt.Fprintln(stdout, map[bool]string{true: "pinned", false: "unpinned"}[on])
		}
		return err
	}
	return errors.New("--pin takes on, off or toggle")
}

func pinned(g *globals) bool {
	v, _ := g.tc.Run("show-option", "-gqv", "@herd-sidebar")
	return isOn(strings.TrimSpace(v))
}

func isOn(v string) bool { return v == "on" || v == "yes" || v == "true" || v == "1" }

func togglePinned(g *globals) (bool, error) {
	on := !pinned(g)
	return on, setPinned(g, on)
}

// setPinned turns persistent sidebars on (in every window, and in new ones
// via hooks) or off (removing them all).
func setPinned(g *globals, on bool) error {
	if !on {
		_, _ = g.tc.Run("set-option", "-g", "@herd-sidebar", "off")
		_, _ = g.tc.Run("set-hook", "-gu", "after-new-window[4210]")
		_, _ = g.tc.Run("set-hook", "-gu", "after-new-session[4210]")
		out, err := g.tc.Run("list-panes", "-a", "-F", "#{pane_id}|~|#{@herd_sidebar}")
		if err != nil {
			return err
		}
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			if id, mark, _ := strings.Cut(l, "|~|"); mark == "1" {
				_, _ = g.tc.Run("kill-pane", "-t", id)
			}
		}
		return nil
	}
	ensure := hooks.ShellQuote(selfExe()) + " -S " + hooks.ShellQuote(g.tmuxSocket) + " sidebar --ensure --window #{window_id}"
	hook := `run-shell -b "` + strings.ReplaceAll(ensure, `"`, `\"`) + `"`
	for _, args := range [][]string{
		{"set-option", "-g", "@herd-sidebar", "on"},
		{"set-hook", "-g", "after-new-window[4210]", hook},
		{"set-hook", "-g", "after-new-session[4210]", hook},
	} {
		if _, err := g.tc.Run(args...); err != nil {
			return err
		}
	}
	out, err := g.tc.Run("list-windows", "-a", "-F", "#{window_id}")
	if err != nil {
		return err
	}
	for _, w := range strings.Fields(out) {
		if err := sidebarPane(g, w, 0, false); err != nil {
			return err
		}
	}
	return nil
}
