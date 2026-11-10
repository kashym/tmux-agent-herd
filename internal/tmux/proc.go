package tmux

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Interpreters whose pane_current_command hides the real program
// (e.g. `node .../gemini`, `python -m aider`, a bash script named claude).
var interpreters = map[string]bool{"node": true, "nodejs": true, "bun": true, "deno": true,
	"python": true, "python3": true, "ruby": true, "perl": true,
	"bash": true, "sh": true, "zsh": true, "dash": true}

// ForegroundArgs returns argv of the foreground process group leader of the
// terminal that panePid (the pane's shell) is attached to.
func ForegroundArgs(panePid int) []string {
	if runtime.GOOS == "linux" {
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(panePid) + "/stat")
		if err != nil {
			return nil
		}
		// Fields after the ")" closing comm: state ppid pgrp session tty_nr tpgid
		i := bytes.LastIndexByte(stat, ')')
		if i < 0 {
			return nil
		}
		f := strings.Fields(string(stat[i+1:]))
		if len(f) < 6 {
			return nil
		}
		tpgid, err := strconv.Atoi(f[5])
		if err != nil || tpgid <= 0 {
			return nil
		}
		cl, err := os.ReadFile("/proc/" + strconv.Itoa(tpgid) + "/cmdline")
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimRight(string(cl), "\x00"), "\x00")
	}
	out, err := exec.Command("ps", "-o", "tpgid=", "-p", strconv.Itoa(panePid)).Output()
	if err != nil {
		return nil
	}
	tp := strings.TrimSpace(string(out))
	if tp == "" || tp == "-1" || tp == "0" {
		return nil
	}
	out, err = exec.Command("ps", "-o", "args=", "-p", tp).Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

// NeedsProcessCheck reports whether cmd may be hiding an agent. On macOS
// shells are skipped because each check costs two ps execs.
func NeedsProcessCheck(cmd string) bool {
	cmd = strings.TrimPrefix(cmd, "-")
	if !interpreters[cmd] {
		return false
	}
	if runtime.GOOS != "linux" && (cmd == "bash" || cmd == "sh" || cmd == "zsh" || cmd == "dash") {
		return false
	}
	return true
}

// AgentInArgs finds an agent name in an interpreter's argv: the script
// basename (sans extension) or a path segment like ".../gemini-cli/...".
func AgentInArgs(argv []string, agents map[string]bool) string {
	if len(argv) < 2 {
		return ""
	}
	for _, a := range argv[1:min(len(argv), 4)] {
		if strings.HasPrefix(a, "-") {
			continue
		}
		base := filepath.Base(a)
		base = strings.TrimSuffix(base, filepath.Ext(base))
		if agents[base] {
			return base
		}
		for _, seg := range strings.Split(filepath.ToSlash(a), "/") {
			s := strings.TrimSuffix(strings.TrimPrefix(seg, "@"), "-cli")
			if agents[s] {
				return s
			}
		}
	}
	return ""
}
