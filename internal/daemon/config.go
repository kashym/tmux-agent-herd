package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kashym/tmux-agent-herd/internal/tmux"
)

// Config mirrors the @herd-* tmux options the daemon cares about.
type Config struct {
	Agents      []string
	Notify      map[string]bool
	NotifyCmd   string
	Poll        time.Duration
	StaleAfter  time.Duration
	PatternDirs []string
}

// Defaults from the design doc.
const (
	DefaultAgents = "claude codex gemini copilot opencode pi aider"
	DefaultNotify = "blocked,done"
)

var optNames = []string{"@herd-agents", "@herd-notify", "@herd-notify-cmd", "@herd-poll-ms", "@herd-stale-after", "@herd-patterns-dir"}

// ConfigFormat is a tmux format that expands to all option values.
func ConfigFormat() string {
	var parts []string
	for _, n := range optNames {
		parts = append(parts, "#{"+n+"}")
	}
	return strings.Join(parts, "|~|")
}

// ParseConfigLine parses an expanded ConfigFormat.
func ParseConfigLine(line string) Config {
	f := strings.Split(strings.TrimRight(line, "\n"), "|~|")
	if len(f) != len(optNames) {
		f = nil
	}
	return ParseConfig(f)
}

// LoadConfig reads the options from the tmux server; unset options get
// defaults. Errors (e.g. no sessions yet) also yield defaults.
func LoadConfig(tc *tmux.Client) Config {
	out, err := tc.Run("display-message", "-p", ConfigFormat())
	if err != nil {
		out = ""
	}
	return ParseConfigLine(out)
}

// ParseConfig builds a Config from option values in optNames order.
func ParseConfig(v []string) Config {
	def := func(i int, d string) string {
		if i < len(v) && strings.TrimSpace(v[i]) != "" {
			return strings.TrimSpace(v[i])
		}
		return d
	}
	c := Config{
		Agents:    strings.Fields(def(0, DefaultAgents)),
		Notify:    map[string]bool{},
		NotifyCmd: def(2, ""),
	}
	if n := def(1, DefaultNotify); n != "off" && n != "none" {
		for _, s := range strings.FieldsFunc(n, func(r rune) bool { return r == ',' || r == ' ' }) {
			c.Notify[s] = true
		}
	}
	ms, err := strconv.Atoi(def(3, "1000"))
	if err != nil || ms < 100 {
		ms = 1000
	}
	c.Poll = time.Duration(ms) * time.Millisecond
	st, err := strconv.Atoi(def(4, "600"))
	if err != nil || st <= 0 {
		st = 600
	}
	c.StaleAfter = time.Duration(st) * time.Second
	c.PatternDirs = []string{filepath.Join(configHome(), "herd", "patterns")}
	if d := def(5, ""); d != "" {
		c.PatternDirs = append(c.PatternDirs, expandHome(d))
	}
	return c
}

func configHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config")
}

// StateDir is where events.jsonl and the daemon log live.
func StateDir() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "herd")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "state", "herd")
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, p[2:])
	}
	return p
}
