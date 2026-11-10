package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strconv"
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
	open := fs.Bool("open", false, "open herd mode as a popup on the client, leaving the status line visible")
	width := fs.Int("width", 0, "popup content width (default @herd-mode-width or 32)")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	if *open {
		return openModePopup(g, *client, *width)
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

// openModePopup docks herd mode on the left, between the status line(s) and
// the other edge of the client, so the window list stays visible.
func openModePopup(g *globals, client string, width int) error {
	var args []string
	if client != "" {
		args = []string{"-c", client}
	}
	out, err := g.tc.Run(append(append([]string{"display-message", "-p"}, args...),
		"#{client_height}|~|#{status}|~|#{status-position}")...)
	if err != nil {
		return err
	}
	f := strings.Split(strings.TrimSpace(out), "|~|")
	if len(f) != 3 {
		return fmt.Errorf("unexpected client info %q", out)
	}
	height, _ := strconv.Atoi(f[0])
	lines := 0
	switch f[1] {
	case "on":
		lines = 1
	case "off", "":
	default:
		lines, _ = strconv.Atoi(f[1])
	}
	top := 0
	if f[2] == "top" {
		top = lines
	}
	h := max(height-lines, 8)
	if width <= 0 {
		v, _ := g.tc.Run("show-option", "-gqv", "@herd-mode-width")
		width, _ = strconv.Atoi(strings.TrimSpace(v))
		if width < 20 {
			width = 32
		}
	}
	popup := append([]string{"display-popup"}, args...)
	// A popup's -y is where its bottom lands: top row + height.
	popup = append(popup, "-E", "-x", "0", "-y", strconv.Itoa(top+h),
		"-w", strconv.Itoa(width+2), "-h", strconv.Itoa(h))
	if tmuxAtLeast(3, 3) {
		popup = append(popup, "-T", " herd ")
	}
	cmd := hooks.ShellQuote(selfExe()) + " -S " + hooks.ShellQuote(g.tmuxSocket) + " mode"
	if client != "" {
		cmd += " --client " + hooks.ShellQuote(client)
	}
	// display-popup only returns when the popup closes; don't wait for it.
	c := exec.Command(g.tc.Bin, append([]string{"-S", g.tc.Socket}, append(popup, cmd)...)...)
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}

func tmuxAtLeast(major, minor int) bool {
	out, err := exec.Command("tmux", "-V").Output()
	if err != nil {
		return true
	}
	var a, b int
	s := strings.TrimLeft(strings.TrimPrefix(strings.TrimSpace(string(out)), "tmux "), "next-")
	if _, err := fmt.Sscanf(s, "%d.%d", &a, &b); err != nil {
		return true // "master" and the like
	}
	return a > major || (a == major && b >= minor)
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
