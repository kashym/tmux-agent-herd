package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kashym/tmux-agent-herd/internal/api"
	"github.com/kashym/tmux-agent-herd/internal/daemon"
	"github.com/kashym/tmux-agent-herd/internal/harness"
)

// cmdDoctor prints everything herd changed in tmux and anything that looks
// wrong, so users can audit it and bug reports start from the same place.
func cmdDoctor(g *globals, args []string, stdout io.Writer) error {
	ok := func(f string, a ...any) { fmt.Fprintf(stdout, "  \x1b[32m✓\x1b[0m "+f+"\n", a...) }
	warn := func(f string, a ...any) { fmt.Fprintf(stdout, "  \x1b[33m!\x1b[0m "+f+"\n", a...) }
	bad := func(f string, a ...any) { fmt.Fprintf(stdout, "  \x1b[31m✗\x1b[0m "+f+"\n", a...) }
	info := func(f string, a ...any) { fmt.Fprintf(stdout, "    "+f+"\n", a...) }
	section := func(s string) { fmt.Fprintf(stdout, "\n\x1b[1m%s\x1b[0m\n", s) }
	opt := func(name string) string {
		v, _ := g.tc.Run("show-option", "-gqv", name)
		return strings.TrimSpace(v)
	}

	fmt.Fprintf(stdout, "herd %s  (%s)\n", version, pluginDir())

	section("tmux")
	if !g.tc.Alive() {
		bad("no tmux server on %s", g.tmuxSocket)
		return nil
	}
	tv, _ := exec.Command("tmux", "-V").Output()
	tver := strings.TrimSpace(string(tv))
	if m := regexp.MustCompile(`(\d+)\.(\d+)`).FindStringSubmatch(tver); m != nil {
		maj, _ := strconv.Atoi(m[1])
		mn, _ := strconv.Atoi(m[2])
		if maj > 3 || (maj == 3 && mn >= 3) {
			ok("%s", tver)
		} else {
			warn("%s: herd is tested on tmux 3.3+", tver)
		}
	}
	ok("server %s", g.tmuxSocket)
	if p := opt("prefix"); p != "" {
		info("prefix is %s", p)
	}

	section("herdd")
	if resp, err := api.Call(g.herdSock, api.Request{Op: api.OpPing}, time.Second); err != nil {
		bad("not running (start: herd daemon --ensure; log: %s)", filepath.Join(daemon.StateDir(), "herdd.log"))
	} else if resp.Version != version {
		warn("running %s but this binary is %s; restart with: herd daemon --stop && herd daemon --ensure", resp.Version, version)
	} else {
		ok("running (pid %d), socket %s", resp.Pid, g.herdSock)
	}
	if panes, err := listPanes(g, false); err == nil {
		ok("%d agent pane(s) detected", len(panes))
	}

	section("What herd changed in tmux")
	if keys := opt("@herd_keys"); keys != "" {
		for _, k := range strings.Fields(keys) {
			key, label, _ := strings.Cut(k, "=")
			ok("bound %s → %s", key, strings.ReplaceAll(label, "_", " "))
		}
	} else {
		info("no keys bound")
	}
	if c := opt("@herd_conflicts"); c != "" {
		for _, k := range strings.Fields(c) {
			owner, _ := g.tc.Run("list-keys", "-T", "prefix", strings.TrimPrefix(k, "prefix+"))
			what := strings.Fields(owner)
			if len(what) > 4 {
				what = what[4:] // drop "bind-key -T prefix KEY"
			}
			warn("%s is yours (%s); herd left it alone. Set @herd-key to another key.", k, oneLine(strings.Join(what, " "), 50))
		}
	}
	switch opt("@herd_status_mode") {
	case "placeholder":
		ok("status line: filled in your #{herd} placeholder")
	case "appended":
		ok("status line: appended the herd summary to status-right (put #{herd} where you want it instead)")
	case "present":
		ok("status line: your status-right already shows @herd_status")
	default:
		info("status line: untouched")
	}
	if !strings.Contains(opt("status-right")+opt("status-left"), "@herd_status") && opt("@herd_status_mode") != "off" {
		warn("the herd summary is not in your status line any more (a theme loaded after herd may have replaced it)")
	}
	switch opt("@herd_badge_mode") {
	case "placeholder":
		ok("window list: filled in your #{herd_badge} placeholder")
	case "prefixed":
		ok("window list: added a state dot in front of window names (@herd-window-badge off to stop)")
	default:
		info("window list: untouched")
	}
	if isOn(opt("@herd-border")) {
		ok("pane borders: pane-border-status top with agent state")
	}
	if env, _ := g.tc.Run("show-environment", "-g", "PATH"); strings.Contains(env, filepath.Join(pluginDir(), "bin")) {
		ok("PATH for new panes: appended %s (@herd-path off to stop)", tildify(filepath.Join(pluginDir(), "bin")))
	}
	hooks, _ := g.tc.Run("show-hooks", "-g")
	whooks, _ := g.tc.Run("show-hooks", "-gw")
	for _, l := range strings.Split(hooks+whooks, "\n") {
		if strings.Contains(l, "[4210]") {
			name, _, _ := strings.Cut(l, " ")
			ok("hook %s", name)
		}
	}
	if pinned(g) {
		ok("herd mode pinned: a sidebar in every window")
	}

	section("Agent CLIs")
	for _, s := range harness.Statuses(pluginDir(), hookPath()) {
		h := s.Harness
		switch {
		case !s.Installed && !s.Configured:
			continue
		case s.Err != nil:
			bad("%s: %v", h.Title, s.Err)
		case s.Configured:
			ok("%s: connected (%s)", h.Title, tildify(h.Target()))
		default:
			warn("%s: installed but not connected; states come from the screen. Run: herd setup %s", h.Title, h.Name)
		}
	}

	section("Other")
	if _, err := exec.LookPath("fzf"); err != nil {
		info("fzf not installed (only the optional picker uses it)")
	}
	if p, err := exec.LookPath("herd"); err == nil {
		if r, _ := filepath.EvalSymlinks(p); r != "" && r != selfExe() && !strings.HasPrefix(r, pluginDir()) {
			warn("`herd` on your PATH is %s, not this plugin (e.g. Laravel Herd). Agents use $HERD_BIN or the skill's absolute path.", p)
		}
	}
	info("state and logs: %s", tildify(daemon.StateDir()))
	return nil
}

// cmdWelcome shows a one-line hint once per machine, to attached clients.
func cmdWelcome(g *globals, _ []string) error {
	marker := filepath.Join(daemon.StateDir(), "welcomed")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	clients, err := g.tc.Run("list-clients", "-F", "#{client_name}")
	if err != nil || strings.TrimSpace(clients) == "" {
		return nil // nobody to show it to yet; the client-attached hook retries
	}
	msg := "herd ready"
	keys, _ := g.tc.Run("show-option", "-gqv", "@herd_keys")
	for _, k := range strings.Fields(keys) {
		if key, label, _ := strings.Cut(k, "="); strings.HasPrefix(label, "herd") {
			msg += " · " + strings.Replace(key, "prefix+", "prefix + ", 1) + " opens herd mode"
		}
	}
	msg += " · herd doctor shows what it changed"
	for _, c := range strings.Fields(clients) {
		_, _ = g.tc.Run("display-message", "-d", "8000", "-c", c, msg)
	}
	_ = os.MkdirAll(filepath.Dir(marker), 0o755)
	return os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644)
}
