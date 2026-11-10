// Integration tests: each test starts an isolated tmux server
// (tmux -L herdtest-<n> -f /dev/null) with a temporary HOME, builds the herd
// binary into a temporary plugin layout, and drives it like a user would.
// They never touch the user's tmux server or ~/.claude.
package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/kashym/tmux-agent-herd/internal/api"
)

var (
	pluginRoot string // temp plugin layout with bin/herd
	repoRoot   string
	serverSeq  atomic.Int32
)

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("tmux"); err != nil {
		fmt.Println("tmux not installed; skipping integration tests")
		os.Exit(0)
	}
	wd, _ := os.Getwd()
	repoRoot = filepath.Dir(wd)
	dir, err := os.MkdirTemp("", "herd-plugin-")
	if err != nil {
		panic(err)
	}
	pluginRoot = dir
	for _, d := range []string{"scripts", "skills", "patterns"} {
		if err := os.Symlink(filepath.Join(repoRoot, d), filepath.Join(dir, d)); err != nil {
			panic(err)
		}
	}
	for _, f := range []string{"agent-herd.tmux", "VERSION"} {
		data, _ := os.ReadFile(filepath.Join(repoRoot, f))
		_ = os.WriteFile(filepath.Join(dir, f), data, 0o755)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "bin", "herd"), "./cmd/herd")
	build.Dir = repoRoot
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic("go build failed: " + err.Error())
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// env is one isolated tmux server + HOME.
type env struct {
	t     *testing.T
	name  string // -L socket name
	sock  string
	home  string
	fake  string // fake agent script dir
	vars  []string
	herdB string
}

func newEnv(t *testing.T, opts ...string) *env {
	t.Helper()
	e := &env{t: t, name: fmt.Sprintf("herdtest-%d-%d", os.Getpid(), serverSeq.Add(1))}
	e.home = t.TempDir()
	e.fake = filepath.Join(e.home, "fake")
	e.herdB = filepath.Join(pluginRoot, "bin", "herd")
	_ = os.MkdirAll(e.fake, 0o755)
	// Fake agent: redraws $FAKE_SCREEN whenever the file changes. Named
	// "claude" so it is detected as an agent; always launched by absolute
	// path so the real claude is never run.
	script := "#!/bin/bash\nf=\"${FAKE_SCREEN:-/dev/null}\"; last=\"\"\n" +
		"while :; do cur=\"$(cat \"$f\" 2>/dev/null)\"; if [ \"$cur\" != \"$last\" ]; then clear; printf '%s\\n' \"$cur\"; last=\"$cur\"; fi; sleep 0.1; done\n"
	if err := os.WriteFile(filepath.Join(e.fake, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e.vars = []string{"HOME=" + e.home, "XDG_STATE_HOME=" + filepath.Join(e.home, "state"),
		"XDG_CONFIG_HOME=" + filepath.Join(e.home, "config"), "CLAUDE_CONFIG_DIR=" + filepath.Join(e.home, ".claude"),
		"TMUX=", "TMUX_PANE=", "HERD_SOCKET=", "HERD_TMUX_SOCKET="}
	e.tmux("-f", "/dev/null", "new-session", "-d", "-s", "main", "-x", "200", "-y", "50")
	e.sock = strings.TrimSpace(e.tmux("display-message", "-p", "#{socket_path}"))
	for i := 0; i+1 < len(opts); i += 2 {
		e.tmux("set-option", "-g", opts[i], opts[i+1])
	}
	t.Cleanup(func() {
		_, _ = e.herdErr(nil, "daemon", "--stop")
		_ = e.cmd("tmux", "-L", e.name, "kill-server").Run()
		_ = os.Remove(e.sock)
		_ = os.Remove(api.SocketPath(e.sock) + ".lock")
	})
	return e
}

func (e *env) cmd(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.Env = append(os.Environ(), e.vars...)
	return c
}

func (e *env) tmux(args ...string) string {
	e.t.Helper()
	out, err := e.cmd("tmux", append([]string{"-L", e.name}, args...)...).CombinedOutput()
	if err != nil {
		e.t.Fatalf("tmux %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// herdErr runs herd; extra env (e.g. TMUX_PANE) may be given.
func (e *env) herdErr(extra []string, args ...string) (string, error) {
	c := e.cmd(e.herdB, append([]string{"-L", e.name}, args...)...)
	c.Env = append(c.Env, extra...)
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	return out.String(), err
}

func (e *env) herd(args ...string) string {
	e.t.Helper()
	out, err := e.herdErr(nil, args...)
	if err != nil {
		e.t.Fatalf("herd %v: %v\n%s", args, err, out)
	}
	return out
}

// inPane returns env vars making herd believe it runs inside pane id.
func (e *env) inPane(id string) []string {
	return []string{"TMUX=" + e.sock + ",1,0", "TMUX_PANE=" + id}
}

func (e *env) startDaemon() {
	e.t.Helper()
	e.herd("daemon", "--ensure")
}

func (e *env) fakeAgent(screenFile, fixture string, extra ...string) string {
	e.t.Helper()
	e.setScreen(screenFile, fixture)
	args := append([]string{"split-window", "-d", "-P", "-F", "#{pane_id}", "-e", "FAKE_SCREEN=" + screenFile}, extra...)
	args = append(args, filepath.Join(e.fake, "claude"))
	return strings.TrimSpace(e.tmux(args...))
}

func (e *env) setScreen(file, fixture string) {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, "test", "fixtures", fixture))
	if err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) opt(pane, format string) string {
	return strings.TrimSpace(e.tmux("display-message", "-p", "-t", pane, format))
}

func (e *env) hook(pane, arg, payload string) {
	e.t.Helper()
	c := e.cmd(filepath.Join(pluginRoot, "scripts", "herd-hook"), arg)
	c.Env = append(c.Env, e.inPane(pane)...)
	c.Stdin = strings.NewReader(payload)
	if out, err := c.CombinedOutput(); err != nil || len(out) > 0 {
		e.t.Fatalf("herd-hook: %v %q", err, out)
	}
}

// eventually polls cond until it returns "" or timeout passes.
func (e *env) eventually(timeout time.Duration, cond func() string) time.Duration {
	e.t.Helper()
	start := time.Now()
	var last string
	for time.Since(start) < timeout {
		if last = cond(); last == "" {
			return time.Since(start)
		}
		time.Sleep(25 * time.Millisecond)
	}
	e.t.Fatalf("condition not met after %v: %s", timeout, last)
	return 0
}

func (e *env) waitState(pane, want string, timeout time.Duration) time.Duration {
	e.t.Helper()
	return e.eventually(timeout, func() string {
		if got := e.opt(pane, "#{@herd_state}"); got != want {
			return fmt.Sprintf("%s: @herd_state=%q want %q", pane, got, want)
		}
		return ""
	})
}

func (e *env) lsJSON() []api.Pane {
	e.t.Helper()
	var panes []api.Pane
	if err := json.Unmarshal([]byte(e.herd("ls", "--json")), &panes); err != nil {
		e.t.Fatal(err)
	}
	return panes
}

// --- Phase 0 ---

func TestLsListsEveryPane(t *testing.T) {
	e := newEnv(t)
	e.tmux("split-window", "-d")
	e.tmux("new-window", "-d")
	e.startDaemon()
	panes := e.lsJSON()
	if len(panes) != 3 {
		t.Fatalf("want 3 panes, got %d: %+v", len(panes), panes)
	}
	for _, p := range panes {
		if p.State != "none" || p.Session != "main" {
			t.Errorf("unexpected row %+v", p)
		}
	}
	out := e.herd("ls")
	if !strings.Contains(out, "PANE") || strings.Count(out, "main:") != 3 {
		t.Errorf("table output:\n%s", out)
	}
	// --ensure is idempotent and a second daemon refuses to start.
	e.startDaemon()
	if out := e.herd("daemon"); !strings.Contains(out, "already running") {
		t.Errorf("second daemon: %q", out)
	}
}

// --- Phase 1 ---

func TestHookSourceAndStatusLine(t *testing.T) {
	e := newEnv(t)
	notes := filepath.Join(e.home, "notes")
	e.tmux("set-option", "-g", "@herd-notify-cmd", `printf '%s|%s\n' "$1" "$2" >> `+notes)
	e.startDaemon()
	screen := filepath.Join(e.home, "screen")
	p := e.fakeAgent(screen, "claude/idle-fresh.txt")
	e.waitState(p, "idle", 3*time.Second)
	if src := e.opt(p, "#{@herd_source}"); src != "process" && src != "screen" {
		t.Errorf("source = %q", src)
	}

	e.hook(p, "working", `{"hook_event_name":"UserPromptSubmit","prompt":"go"}`)
	d := e.waitState(p, "working", time.Second)
	t.Logf("hook→status latency: %v", d)
	if s := e.tmux("show-option", "-gv", "@herd_status"); !strings.Contains(s, "◐ 1 working") {
		t.Errorf("@herd_status = %q", s)
	}

	e.hook(p, "blocked", `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"}`)
	e.waitState(p, "blocked", time.Second)
	if k, m := e.opt(p, "#{@herd_kind}"), e.opt(p, "#{@herd_msg}"); k != "permission" || m != "Claude needs your permission to use Bash" {
		t.Errorf("kind=%q msg=%q", k, m)
	}
	if s := e.tmux("show-option", "-gv", "@herd_status"); !strings.Contains(s, "● 1 blocked") {
		t.Errorf("@herd_status = %q", s)
	}
	if b := e.opt(p, "#{@herd_window_badge}"); !strings.Contains(b, "●") {
		t.Errorf("window badge = %q", b)
	}
	// idle_prompt notifications must not flip the state.
	e.hook(p, "blocked", `{"hook_event_name":"Notification","notification_type":"idle_prompt","message":"Claude is waiting for your input"}`)

	e.hook(p, "done", `{"hook_event_name":"Stop"}`)
	e.waitState(p, "done", time.Second)

	// Notifications fired for the unfocused pane (rate limit: one per 30 s,
	// so only the blocked one).
	e.eventually(2*time.Second, func() string {
		b, _ := os.ReadFile(notes)
		if !strings.Contains(string(b), "claude blocked|main:0.1: Claude needs your permission to use Bash") {
			return "notification file: " + string(b)
		}
		return ""
	})

	// Focus decays done to idle.
	e.herd("_focus", p)
	e.waitState(p, "idle", time.Second)

	// SessionEnd clears the hook source; process detection still sees claude.
	e.hook(p, "none", `{"hook_event_name":"SessionEnd"}`)
	e.waitState(p, "idle", time.Second)
	if src := e.opt(p, "#{@herd_source}"); src == "hook" {
		t.Errorf("hook source should be cleared")
	}

	// Agent exits → options cleared.
	e.tmux("respawn-pane", "-k", "-t", p, "bash")
	e.waitState(p, "", 3*time.Second)

	log := e.herd("log", p)
	for _, want := range []string{"none → idle", "→ working", "working → blocked", "blocked → done", "done → idle", "source=hook"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
}

func TestHookOutsideTmuxIsSilent(t *testing.T) {
	c := exec.Command(filepath.Join(pluginRoot, "scripts", "herd-hook"), "working")
	c.Env = append(os.Environ(), "TMUX=", "TMUX_PANE=")
	out, err := c.CombinedOutput()
	if err != nil || len(out) != 0 {
		t.Fatalf("err=%v out=%q", err, out)
	}
	// Daemon not running: still silent and exit 0.
	e := newEnv(t)
	c = exec.Command(filepath.Join(pluginRoot, "scripts", "herd-hook"), "working")
	c.Env = append(os.Environ(), e.inPane("%0")...)
	c.Env = append(c.Env, e.vars[:4]...)
	out, err = c.CombinedOutput()
	if err != nil || len(out) != 0 {
		t.Fatalf("no daemon: err=%v out=%q", err, out)
	}
}

// --- Phase 2 ---

func TestJumpAndPicker(t *testing.T) {
	e := newEnv(t)
	e.startDaemon()
	// 4 agents in 3 windows.
	var panes []string
	for i := 0; i < 4; i++ {
		f := filepath.Join(e.home, fmt.Sprintf("s%d", i))
		var target []string
		switch i {
		case 1:
			e.tmux("new-window", "-d", "-t", "main:1")
			target = []string{"-t", "main:1"}
		case 2, 3:
			if i == 2 {
				e.tmux("new-window", "-d", "-t", "main:2")
			}
			target = []string{"-t", "main:2"}
		}
		panes = append(panes, e.fakeAgent(f, "claude/idle-fresh.txt", target...))
	}
	for _, p := range panes {
		e.waitState(p, "idle", 3*time.Second)
	}
	e.hook(panes[0], "working", `{"hook_event_name":"UserPromptSubmit"}`)
	e.hook(panes[3], "blocked", `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Approve Edit"}`)
	e.hook(panes[1], "done", `{"hook_event_name":"Stop"}`)
	e.waitState(panes[3], "blocked", time.Second)

	list := e.herd("pick", "--list")
	lines := strings.Split(strings.TrimSpace(list), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], panes[3]) || !strings.HasPrefix(lines[1], panes[1]) {
		t.Fatalf("picker order wrong:\n%s", list)
	}

	e.herd("jump", "blocked")
	if cur := e.opt("main:", "#{pane_id}"); cur != panes[3] {
		t.Fatalf("jump blocked: active pane %s, want %s", cur, panes[3])
	}
	e.herd("jump", "done")
	if cur := e.opt("main:", "#{pane_id}"); cur != panes[1] {
		t.Fatalf("jump done: active pane %s, want %s", cur, panes[1])
	}
	// Jumping there marked it seen.
	e.waitState(panes[1], "idle", time.Second)
	// Nothing done left: jump is a no-op with a message, not an error.
	e.herd("jump", "done")
}

// --- Phase 3 ---

func TestScreenHeuristics(t *testing.T) {
	e := newEnv(t, "@herd-poll-ms", "200")
	e.startDaemon()
	f := filepath.Join(e.home, "screen")
	p := e.fakeAgent(f, "claude/idle-fresh.txt")
	e.waitState(p, "idle", 3*time.Second)
	e.setScreen(f, "claude/working-thinking.txt")
	e.waitState(p, "working", 2*time.Second)
	e.setScreen(f, "claude/blocked.permission-bash.txt")
	e.waitState(p, "blocked", 2*time.Second)
	if k, m, s := e.opt(p, "#{@herd_kind}"), e.opt(p, "#{@herd_msg}"), e.opt(p, "#{@herd_source}"); k != "permission" || m != "Do you want to proceed?" || s != "screen" {
		t.Errorf("kind=%q msg=%q source=%q", k, m, s)
	}
	e.setScreen(f, "claude/working-thinking.txt")
	e.waitState(p, "working", 2*time.Second)
	e.setScreen(f, "claude/idle-done.txt")
	e.waitState(p, "done", 2*time.Second)
	// A fresh hook silences the heuristics.
	e.hook(p, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	e.waitState(p, "working", time.Second)
	e.setScreen(f, "claude/blocked.permission-bash.txt")
	time.Sleep(800 * time.Millisecond)
	if s := e.opt(p, "#{@herd_state}"); s != "working" {
		t.Errorf("hook should win over screen, got %s", s)
	}
}

func TestCPUWithTenAgents(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	if runtime.GOOS != "linux" {
		t.Skip("uses /proc")
	}
	e := newEnv(t)
	e.startDaemon()
	fixtures := []string{"claude/idle-fresh.txt", "claude/working-thinking.txt", "claude/blocked.permission-bash.txt", "claude/idle-done.txt"}
	var panes []string
	for i := 0; i < 10; i++ {
		if i%3 == 0 {
			e.tmux("new-window", "-d")
		}
		f := filepath.Join(e.home, fmt.Sprintf("cpu%d", i))
		panes = append(panes, e.fakeAgent(f, fixtures[i%len(fixtures)], "-t", "main:"))
		e.tmux("select-layout", "-t", "main:", "tiled")
	}
	for _, p := range panes {
		e.eventually(3*time.Second, func() string {
			if e.opt(p, "#{@herd_state}") == "" {
				return p + " not detected"
			}
			return ""
		})
	}
	resp, err := api.Call(api.SocketPath(e.sock), api.Request{Op: api.OpPing}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	serverPid, _ := strconv.Atoi(e.opt("main:", "#{pid}"))
	d0, s0 := cpuTicks(t, resp.Pid), cpuTicks(t, serverPid)
	const window = 10 * time.Second
	time.Sleep(window)
	d1, s1 := cpuTicks(t, resp.Pid), cpuTicks(t, serverPid)
	hz := 100.0 // USER_HZ
	daemonPct := float64(d1-d0) / hz / window.Seconds() * 100
	serverPct := float64(s1-s0) / hz / window.Seconds() * 100
	t.Logf("10 agent panes, 1 s poll: herdd %.2f%% CPU (incl. tmux client children), tmux server %.2f%%", daemonPct, serverPct)
	if daemonPct >= 1.0 {
		t.Errorf("herdd CPU %.2f%% >= 1%%", daemonPct)
	}
}

// cpuTicks returns utime+stime+cutime+cstime for pid.
func cpuTicks(t *testing.T, pid int) int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+2:]))
	var sum int64
	for _, i := range []int{11, 12, 13, 14} {
		v, _ := strconv.ParseInt(f[i], 10, 64)
		sum += v
	}
	return sum
}

// --- Phase 4 ---

func TestSpawnSendReadWaitClose(t *testing.T) {
	e := newEnv(t)
	e.startDaemon()
	me := e.inPane("%0")

	out, err := e.herdErr(me, "spawn", "--name", "worker", "--json", "--", "cat")
	if err != nil {
		t.Fatal(out)
	}
	var sp map[string]string
	_ = json.Unmarshal([]byte(out), &sp)
	w := sp["pane_id"]
	if !strings.HasPrefix(w, "%") {
		t.Fatalf("spawn output %q", out)
	}
	if out, err := e.herdErr(me, "send", "worker", "hello herd", "--enter"); err != nil {
		t.Fatal(out)
	}
	e.eventually(2*time.Second, func() string {
		out, _ := e.herdErr(nil, "read", w, "--lines", "5")
		if strings.Count(out, "hello herd") < 2 { // typed line + cat's echo
			return "read: " + out
		}
		return ""
	})
	var rd struct {
		Lines []string `json:"lines"`
	}
	_ = json.Unmarshal([]byte(e.herd("read", "worker", "--json")), &rd)
	if len(rd.Lines) == 0 {
		t.Error("read --json returned no lines")
	}

	// Multi-line text goes through a paste buffer.
	if out, err := e.herdErr(me, "send", w, "line one\nline two"); err != nil {
		t.Fatal(out)
	}
	e.eventually(2*time.Second, func() string {
		if out, _ := e.herdErr(nil, "read", w); !strings.Contains(out, "line two") {
			return out
		}
		return ""
	})

	// Safety: no session → refuse; other session → refuse; own pane → refuse.
	if _, err := e.herdErr(nil, "send", w, "x"); err == nil {
		t.Error("send outside tmux without --any-session should fail")
	}
	e.tmux("new-session", "-d", "-s", "other")
	other := strings.TrimSpace(e.tmux("display-message", "-p", "-t", "other:", "#{pane_id}"))
	if out, err := e.herdErr(me, "close", other); err == nil || !strings.Contains(out, "another tmux session") {
		t.Errorf("close other session: %v %s", err, out)
	}
	if out, err := e.herdErr(me, "close", "%0", "--any-session"); err == nil || !strings.Contains(out, "own pane") {
		t.Errorf("close own pane: %v %s", err, out)
	}
	if out, err := e.herdErr(me, "close", other, "--any-session"); err != nil {
		t.Errorf("close --any-session: %v %s", err, out)
	}

	// send refuses while the agent is working.
	f := filepath.Join(e.home, "screen")
	a := e.fakeAgent(f, "claude/idle-fresh.txt")
	e.waitState(a, "idle", 3*time.Second)
	e.hook(a, "working", `{"hook_event_name":"UserPromptSubmit"}`)
	e.waitState(a, "working", time.Second)
	if out, err := e.herdErr(me, "send", a, "more"); err == nil || !strings.Contains(out, "is working") {
		t.Errorf("send to working pane: %v %s", err, out)
	}

	// wait returns when the state arrives.
	done := make(chan string, 1)
	go func() {
		out, err := e.herdErr(nil, "wait", a, "--for", "done", "--timeout", "5")
		done <- fmt.Sprintf("%v|%s", err, out)
	}()
	time.Sleep(300 * time.Millisecond)
	e.hook(a, "done", `{"hook_event_name":"Stop"}`)
	select {
	case r := <-done:
		if !strings.HasPrefix(r, "<nil>|"+a+" done") {
			t.Errorf("wait result %q", r)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("wait did not return")
	}

	// After a send, the old done no longer satisfies wait.
	if out, err := e.herdErr(me, "send", a, "next task", "--enter"); err != nil {
		t.Fatal(out)
	}
	out, err = e.herdErr(nil, "wait", a, "--for", "done", "--timeout", "0.5")
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 2 || !strings.Contains(out, "timed out") {
		t.Errorf("want timeout exit 2, got %v %q", err, out)
	}
	e.hook(a, "done", `{"hook_event_name":"Stop"}`)
	if out, err := e.herdErr(nil, "wait", a, "--for", "done", "--timeout", "2", "--json"); err != nil || !strings.Contains(out, `"state": "done"`) {
		t.Errorf("wait after new done: %v %s", err, out)
	}

	// focus and close.
	e.herd("focus", w)
	if cur := e.opt("main:", "#{pane_id}"); cur != w {
		t.Errorf("focus: active %s want %s", cur, w)
	}
	if out, err := e.herdErr(me, "close", "worker"); err != nil {
		t.Fatal(out)
	}
	if _, err := e.herdErr(nil, "read", w); err == nil {
		t.Error("pane should be gone")
	}
	if log := e.herd("log", "--json"); !strings.Contains(log, `"source":"hook"`) {
		t.Errorf("log --json: %s", log)
	}
}

func TestRunStartsAgentInShell(t *testing.T) {
	e := newEnv(t)
	e.startDaemon()
	out, err := e.herdErr(e.inPane("%0"), "run", "--name", "a1", filepath.Join(e.fake, "claude"))
	if err != nil {
		t.Fatal(out)
	}
	id := strings.TrimSpace(out)
	e.waitState(id, "idle", 5*time.Second)
	if n := e.opt(id, "#{@herd_name}"); n != "a1" {
		t.Errorf("@herd_name = %q", n)
	}
}

// --- setup / skill ---

func TestSetupClaudeAndInstallSkill(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.home, ".claude")
	_ = os.MkdirAll(dir, 0o755)
	orig := "{\n  \"model\": \"opus\",\n  \"hooks\": {\n    \"Stop\": [\n      {\n        \"hooks\": [\n          {\n            \"type\": \"command\",\n            \"command\": \"say done\"\n          }\n        ]\n      }\n    ]\n  }\n}\n"
	_ = os.WriteFile(filepath.Join(dir, "settings.json"), []byte(orig), 0o600)

	// Without a TTY and without --yes nothing is written.
	c := e.cmd(e.herdB, "setup", "claude")
	c.Stdin = nil
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // no controlling tty
	out, _ := c.CombinedOutput()
	if b, _ := os.ReadFile(filepath.Join(dir, "settings.json")); string(b) != orig {
		t.Fatalf("settings changed without confirmation:\n%s", out)
	}
	if !strings.Contains(string(out), "+") {
		t.Errorf("diff not shown:\n%s", out)
	}

	out2 := e.herd("setup", "claude", "--yes")
	if !strings.Contains(out2, "Backup:") {
		t.Errorf("no backup reported:\n%s", out2)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	s := string(b)
	if !strings.Contains(s, "say done") || !strings.Contains(s, "herd-hook --agent claude done") || !strings.HasPrefix(s, "{\n  \"model\": \"opus\"") {
		t.Errorf("merged settings:\n%s", s)
	}
	if st, _ := os.Stat(filepath.Join(dir, "settings.json")); st.Mode().Perm() != 0o600 {
		t.Errorf("mode changed to %v", st.Mode().Perm())
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "settings.json.herd-backup-*"))
	if len(backups) != 1 {
		t.Errorf("backups: %v", backups)
	}
	if out := e.herd("setup", "claude", "--yes"); !strings.Contains(out, "already set up") {
		t.Errorf("second run: %s", out)
	}

	e.herd("install-skill")
	if _, err := os.Stat(filepath.Join(dir, "skills", "herd", "SKILL.md")); err != nil {
		t.Error(err)
	}
}

// --- plugin entry point ---

func TestPluginEntryPoint(t *testing.T) {
	e := newEnv(t, "@herd-border", "on")
	e.tmux("run-shell", filepath.Join(pluginRoot, "agent-herd.tmux"))
	keys := e.tmux("list-keys", "-T", "prefix")
	if !strings.Contains(keys, "mode --client") {
		t.Errorf("herd mode binding missing:\n%s", keys)
	}
	for _, k := range []string{"jump blocked", "jump done", "sidebar --toggle"} {
		if strings.Contains(keys, k) {
			t.Errorf("%q should not be bound by default", k)
		}
	}
	if sr := e.tmux("show-option", "-gv", "status-right"); !strings.Contains(sr, "#{E:@herd_status}") {
		t.Errorf("status-right = %q", sr)
	}
	if wf := e.tmux("show-option", "-gv", "window-status-format"); !strings.Contains(wf, "@herd_window_badge") {
		t.Errorf("window-status-format = %q", wf)
	}
	if bs := e.tmux("show-option", "-gv", "pane-border-status"); strings.TrimSpace(bs) != "top" {
		t.Errorf("pane-border-status = %q", bs)
	}
	hooks := e.tmux("show-hooks", "-g") + e.tmux("show-hooks", "-gw")
	if !strings.Contains(hooks, "client-attached[4210]") || !strings.Contains(hooks, "pane-focus-in[4210]") {
		t.Errorf("hooks:\n%s", hooks)
	}
	if _, err := api.Call(api.SocketPath(e.sock), api.Request{Op: api.OpPing}, 2*time.Second); err != nil {
		t.Fatalf("daemon not running after plugin load: %v", err)
	}
	// Re-sourcing does not duplicate segments.
	e.tmux("run-shell", filepath.Join(pluginRoot, "agent-herd.tmux"))
	if sr := e.tmux("show-option", "-gv", "status-right"); strings.Count(sr, "@herd_status") != 2 {
		t.Errorf("status-right after re-source = %q", sr)
	}
	// Border shows agent state.
	f := filepath.Join(e.home, "screen")
	p := e.fakeAgent(f, "claude/blocked.permission-edit.txt")
	e.waitState(p, "blocked", 3*time.Second)
	border := e.opt(p, "#{E:pane-border-format}")
	if !strings.Contains(border, "claude · ") || !strings.Contains(border, "blocked") || !strings.Contains(border, "main.go?") {
		t.Errorf("border = %q", border)
	}
	if st := e.opt(p, "#{E:@herd_status}"); !strings.Contains(st, "1 blocked") {
		t.Errorf("status = %q", st)
	}
}

// --- harness-neutral paths ---

// fakeNamed starts the fake agent script under another agent's name.
func (e *env) fakeNamed(name, screenFile, fixture string) string {
	e.t.Helper()
	src, _ := os.ReadFile(filepath.Join(e.fake, "claude"))
	bin := filepath.Join(e.fake, name)
	if err := os.WriteFile(bin, src, 0o755); err != nil {
		e.t.Fatal(err)
	}
	e.setScreen(screenFile, fixture)
	return strings.TrimSpace(e.tmux("split-window", "-d", "-P", "-F", "#{pane_id}", "-e", "FAKE_SCREEN="+screenFile, bin))
}

func (e *env) hookArgs(pane string, args ...string) {
	e.t.Helper()
	c := e.cmd(filepath.Join(pluginRoot, "scripts", "herd-hook"), args...)
	c.Env = append(c.Env, e.inPane(pane)...)
	if out, err := c.CombinedOutput(); err != nil || len(out) > 0 {
		e.t.Fatalf("herd-hook %v: %v %q", args, err, out)
	}
}

func TestExtensionStyleHooks(t *testing.T) {
	e := newEnv(t, "@herd-poll-ms", "200")
	e.startDaemon()
	// pi / OpenCode style: explicit agent, state, message and kind; no stdin.
	p := e.fakeNamed("pi", filepath.Join(e.home, "pi"), "claude/idle-fresh.txt")
	e.waitState(p, "idle", 3*time.Second)
	if a := e.opt(p, "#{@herd_agent}"); a != "pi" {
		t.Errorf("agent = %q", a)
	}
	e.hookArgs(p, "--agent", "pi", "working")
	e.waitState(p, "working", time.Second)
	e.hookArgs(p, "--agent", "pi", "--msg", "Pick a file", "--kind", "question", "blocked")
	e.waitState(p, "blocked", time.Second)
	if k, m := e.opt(p, "#{@herd_kind}"), e.opt(p, "#{@herd_msg}"); k != "question" || m != "Pick a file" {
		t.Errorf("kind=%q msg=%q", k, m)
	}
	e.hookArgs(p, "--agent", "pi", "--msg", "402: out of credits", "error")
	e.waitState(p, "error", time.Second)
	if s := e.tmux("show-option", "-gv", "@herd_status"); !strings.Contains(s, "1 error") {
		t.Errorf("status = %q", s)
	}

	// aider style: a completion-only signal reported as a screen event, so
	// screen heuristics keep detecting the next working phase.
	a := e.fakeNamed("aider", filepath.Join(e.home, "aider"), "aider/working.txt")
	e.waitState(a, "working", 3*time.Second)
	e.hookArgs(a, "--agent", "aider", "--source", "screen", "done")
	e.waitState(a, "done", time.Second)
	e.setScreen(filepath.Join(e.home, "aider"), "aider/idle.txt")
	time.Sleep(600 * time.Millisecond)
	if s := e.opt(a, "#{@herd_state}"); s != "done" {
		t.Errorf("done should stick on an idle screen, got %s", s)
	}
	e.setScreen(filepath.Join(e.home, "aider"), "aider/working.txt")
	e.waitState(a, "working", 2*time.Second)

	// A hook from an unnamed agent falls back to the pane's command.
	e.hookArgs(p, "done")
	e.waitState(p, "done", time.Second)
	if ag := e.opt(p, "#{@herd_agent}"); ag != "pi" {
		t.Errorf("agent after unnamed hook = %q", ag)
	}
}

func TestSetupAllHarnesses(t *testing.T) {
	e := newEnv(t)
	// Pretend codex, pi and opencode are installed (and nothing else).
	bins := filepath.Join(e.home, "bins")
	_ = os.MkdirAll(bins, 0o755)
	for _, b := range []string{"codex", "pi", "opencode"} {
		_ = os.WriteFile(filepath.Join(bins, b), []byte("#!/bin/sh\n"), 0o755)
	}
	path := "PATH=" + bins + ":/usr/bin:/bin"
	run := func(args ...string) string {
		out, err := e.herdErr([]string{path}, args...)
		if err != nil {
			t.Fatalf("herd %v: %v\n%s", args, err, out)
		}
		return out
	}
	list := run("setup", "--list")
	for _, h := range []string{"claude", "codex", "gemini", "copilot", "opencode", "pi", "aider"} {
		if !strings.Contains(list, h) {
			t.Errorf("--list missing %s:\n%s", h, list)
		}
	}
	out := run("setup", "all", "--yes")
	for _, f := range []string{".codex/hooks.json", ".pi/agent/extensions/herd.ts", "config/opencode/plugins/herd.js"} {
		if _, err := os.Stat(filepath.Join(e.home, f)); err != nil {
			t.Errorf("%s not written: %v\n%s", f, err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(e.home, ".claude", "settings.json")); err == nil {
		t.Error("claude is not installed; its settings must not be created by 'all'")
	}
	if !strings.Contains(out, "/hooks") {
		t.Errorf("codex trust note missing:\n%s", out)
	}
	if again := run("setup", "all", "--yes"); strings.Count(again, "already set up") != 3 {
		t.Errorf("second run should be a no-op:\n%s", again)
	}
	// Explicit names work even when not installed.
	run("setup", "copilot", "gemini", "--yes")
	for _, f := range []string{".copilot/hooks/herd.json", ".gemini/settings.json"} {
		if _, err := os.Stat(filepath.Join(e.home, f)); err != nil {
			t.Errorf("%s not written", f)
		}
	}
	skills := run("install-skill")
	for _, d := range []string{".codex/skills/herd/SKILL.md", ".pi/agent/skills/herd/SKILL.md", "config/opencode/skills/herd/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(e.home, d)); err != nil {
			t.Errorf("skill %s missing:\n%s", d, skills)
		}
	}
}

// --- sidebar ---

func (e *env) sidebarPane(window string) string {
	e.t.Helper()
	for _, l := range strings.Split(strings.TrimSpace(e.tmux("list-panes", "-t", window, "-F", "#{pane_id} #{@herd_sidebar}")), "\n") {
		if id, mark, _ := strings.Cut(l, " "); mark == "1" {
			return id
		}
	}
	return ""
}

func TestSidebar(t *testing.T) {
	e := newEnv(t)
	e.startDaemon()
	a := e.fakeNamed("codex", filepath.Join(e.home, "a"), "claude/idle-fresh.txt")
	b := e.fakeNamed("pi", filepath.Join(e.home, "b"), "claude/idle-fresh.txt")
	e.waitState(a, "idle", 3*time.Second)
	e.waitState(b, "idle", 3*time.Second)
	e.hookArgs(a, "--agent", "codex", "--kind", "permission", "--msg", "Bash: rm -rf dist", "blocked")
	e.hookArgs(b, "--agent", "pi", "working")

	me := e.inPane("%0")
	if out, err := e.herdErr(me, "sidebar", "--toggle"); err != nil {
		t.Fatal(out)
	}
	sb := e.sidebarPane("main:0")
	if sb == "" {
		t.Fatal("sidebar pane not created")
	}
	if w := e.opt(sb, "#{pane_width}"); w != "32" {
		t.Errorf("width = %s", w)
	}
	var screen string
	e.eventually(3*time.Second, func() string {
		screen = e.tmux("capture-pane", "-p", "-t", sb)
		for _, want := range []string{"herd", "SESSIONS", "main", "AGENTS 2", "codex · permission", "Bash: rm -rf dist", "pi"} {
			if !strings.Contains(screen, want) {
				return "missing " + want + " in:\n" + screen
			}
		}
		return ""
	})
	// Blocked agent sorts first.
	if strings.Index(screen, "codex") > strings.Index(screen, "◐ pi") {
		t.Errorf("blocked agent should be listed first:\n%s", screen)
	}
	// Items: session main, codex, pi. Two downs + Enter focuses pi.
	e.tmux("send-keys", "-t", sb, "j", "j", "Enter")
	e.eventually(2*time.Second, func() string {
		if cur := e.opt("main:", "#{pane_id}"); cur != b {
			return "active pane " + cur + ", want " + b
		}
		return ""
	})
	// Toggle closes it.
	if out, err := e.herdErr(me, "sidebar", "--toggle"); err != nil {
		t.Fatal(out)
	}
	e.eventually(2*time.Second, func() string {
		if e.sidebarPane("main:0") != "" {
			return "sidebar still open"
		}
		return ""
	})
}

func TestSidebarAlwaysOn(t *testing.T) {
	e := newEnv(t, "@herd-sidebar", "on")
	e.tmux("run-shell", filepath.Join(pluginRoot, "agent-herd.tmux"))
	e.eventually(3*time.Second, func() string {
		if e.sidebarPane("main:0") == "" {
			return "no sidebar in existing window"
		}
		return ""
	})
	w := strings.TrimSpace(e.tmux("new-window", "-d", "-P", "-F", "#{window_id}"))
	e.eventually(3*time.Second, func() string {
		if e.sidebarPane(w) == "" {
			return "no sidebar in new window"
		}
		return ""
	})
	// When the last real pane closes, the sidebar exits and the window goes.
	for _, l := range strings.Split(strings.TrimSpace(e.tmux("list-panes", "-t", w, "-F", "#{pane_id} #{@herd_sidebar}")), "\n") {
		if id, mark, _ := strings.Cut(l, " "); mark != "1" {
			e.tmux("kill-pane", "-t", id)
		}
	}
	e.eventually(3*time.Second, func() string {
		if strings.Contains(e.tmux("list-windows", "-F", "#{window_id}"), w) {
			return "window with only a sidebar should close"
		}
		return ""
	})
}

// --- tmux citizenship ---

func TestNeverClobbersBindings(t *testing.T) {
	e := newEnv(t)
	// A C-a user's classic setup, plus a binding left by an older herd.
	e.tmux("set-option", "-g", "prefix", "C-a")
	e.tmux("bind-key", "a", "send-prefix")
	e.tmux("bind-key", "D", "run-shell", "/old/plugin/bin/herd jump done")
	e.tmux("run-shell", filepath.Join(pluginRoot, "agent-herd.tmux"))

	if k := e.tmux("list-keys", "-T", "prefix", "a"); !strings.Contains(k, "send-prefix") {
		t.Errorf("prefix+a was taken over: %s", k)
	}
	if c := strings.TrimSpace(e.tmux("show-option", "-gqv", "@herd_conflicts")); c != "prefix+a" {
		t.Errorf("@herd_conflicts = %q", c)
	}
	if k := e.tmux("list-keys", "-T", "prefix", "D"); !strings.Contains(k, "choose-client") {
		t.Errorf("old herd binding on D should give way to tmux's default: %s", k)
	}
	// Choosing a free key binds it.
	e.tmux("set-option", "-g", "@herd-key", "H")
	e.tmux("run-shell", filepath.Join(pluginRoot, "agent-herd.tmux"))
	if k := e.tmux("list-keys", "-T", "prefix", "H"); !strings.Contains(k, "mode --client") {
		t.Errorf("prefix+H not bound to herd mode: %s", k)
	}
	if c := strings.TrimSpace(e.tmux("show-option", "-gqv", "@herd_conflicts")); c != "" {
		t.Errorf("conflicts should be empty now: %q", c)
	}
	out := e.herd("doctor")
	for _, want := range []string{"bound prefix+H → herd mode", "herdd", "running"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor missing %q:\n%s", want, out)
		}
	}
}

func TestStatusPlaceholders(t *testing.T) {
	e := newEnv(t)
	e.tmux("set-option", "-g", "status-right", "#{herd} | %H:%M")
	e.tmux("set-option", "-g", "window-status-format", "#{herd_badge}#I:#W")
	e.tmux("run-shell", filepath.Join(pluginRoot, "agent-herd.tmux"))
	sr := strings.TrimSpace(e.tmux("show-option", "-gv", "status-right"))
	if strings.Contains(sr, "#{herd}") || strings.Count(sr, "@herd_status") != 2 || !strings.HasSuffix(sr, "| %H:%M") {
		t.Errorf("status-right = %q", sr)
	}
	if m := strings.TrimSpace(e.tmux("show-option", "-gqv", "@herd_status_mode")); m != "placeholder" {
		t.Errorf("status mode %q", m)
	}
	wf := strings.TrimSpace(e.tmux("show-option", "-gv", "window-status-format"))
	if strings.Contains(wf, "#{herd_badge}") || !strings.HasSuffix(wf, "#I:#W") {
		t.Errorf("window-status-format = %q", wf)
	}
	// Current-window format had no placeholder and must stay untouched.
	if cf := e.tmux("show-option", "-gv", "window-status-current-format"); strings.Contains(cf, "herd") {
		t.Errorf("current format touched: %q", cf)
	}
	// @herd-path off leaves PATH alone.
	e2 := newEnv(t, "@herd-path", "off")
	e2.tmux("run-shell", filepath.Join(pluginRoot, "agent-herd.tmux"))
	if env := e2.tmux("show-environment", "-g", "PATH"); strings.Contains(env, filepath.Join(pluginRoot, "bin")) {
		t.Error("PATH changed despite @herd-path off")
	}
}

func TestHerdModeUIAndPin(t *testing.T) {
	e := newEnv(t)
	e.startDaemon()
	a := e.fakeNamed("codex", filepath.Join(e.home, "a"), "claude/idle-fresh.txt")
	b := e.fakeNamed("pi", filepath.Join(e.home, "b"), "claude/idle-fresh.txt")
	e.waitState(a, "idle", 3*time.Second)
	e.waitState(b, "idle", 3*time.Second)
	e.hookArgs(a, "--agent", "codex", "--msg", "Bash: ls", "--kind", "permission", "blocked")

	// The popup UI is the same program; run it in a window of its own.
	ui := strings.TrimSpace(e.tmux("new-window", "-d", "-P", "-F", "#{pane_id}", e.herdB+" -L "+e.name+" mode"))
	e.eventually(3*time.Second, func() string {
		if s := e.tmux("capture-pane", "-p", "-t", ui); !strings.Contains(s, "AGENTS 2") || !strings.Contains(s, "esc") {
			return "mode UI not drawn:\n" + s
		}
		return ""
	})
	// Filter to pi, accept, jump: the UI exits and pi is focused.
	e.tmux("send-keys", "-t", ui, "/")
	e.tmux("send-keys", "-t", ui, "-l", "pi")
	e.tmux("send-keys", "-t", ui, "Enter")
	e.eventually(2*time.Second, func() string {
		if s := e.tmux("capture-pane", "-p", "-t", ui); !strings.Contains(s, "AGENTS 1") {
			return "filter not applied:\n" + s
		}
		return ""
	})
	e.tmux("send-keys", "-t", ui, "Enter")
	e.eventually(3*time.Second, func() string {
		if cur := e.opt("main:0", "#{pane_id}"); cur != b {
			return "active " + cur + " want " + b
		}
		if strings.Contains(e.tmux("list-panes", "-a", "-F", "#{pane_id}"), ui+"\n") {
			return "mode UI should exit after jumping"
		}
		return ""
	})

	// Pin: a sidebar in every window, and in new ones; unpin removes them.
	e.tmux("new-window", "-d")
	e.herd("mode", "--pin", "on")
	windows := strings.Fields(e.tmux("list-windows", "-a", "-F", "#{window_id}"))
	for _, w := range windows {
		if e.sidebarPane(w) == "" {
			t.Errorf("window %s has no sidebar after pin", w)
		}
	}
	nw := strings.TrimSpace(e.tmux("new-window", "-d", "-P", "-F", "#{window_id}"))
	e.eventually(3*time.Second, func() string {
		if e.sidebarPane(nw) == "" {
			return "new window has no sidebar"
		}
		return ""
	})
	if out := e.herd("mode", "--pin", "toggle"); !strings.Contains(out, "unpinned") {
		t.Errorf("toggle output %q", out)
	}
	if strings.Contains(e.tmux("list-panes", "-a", "-F", "#{@herd_sidebar}"), "1") {
		t.Error("sidebars left after unpin")
	}
}

func TestSetupPrintWritesNothing(t *testing.T) {
	e := newEnv(t)
	out := e.herd("setup", "--print", "codex", "pi")
	if !strings.Contains(out, "hooks.json should contain") || !strings.Contains(out, "--agent codex working") || !strings.Contains(out, `"--agent", "pi"`) {
		t.Errorf("print output:\n%s", out)
	}
	for _, f := range []string{".codex/hooks.json", ".pi/agent/extensions/herd.ts"} {
		if _, err := os.Stat(filepath.Join(e.home, f)); err == nil {
			t.Errorf("%s was written", f)
		}
	}
	// The installed skill names this exact binary.
	e.herd("install-skill", "claude")
	b, _ := os.ReadFile(filepath.Join(e.home, ".claude", "skills", "herd", "SKILL.md"))
	if !strings.Contains(string(b), e.herdB) || strings.Contains(string(b), "<!-- herd-bin -->") {
		t.Errorf("skill does not carry the binary path:\n%s", b[:min(len(b), 600)])
	}
}
