// Package harness knows how to connect each supported agent harness to herd:
// where its config lives, how to install herd's hook adapter into it, and
// where it looks for skills. The rest of herd is harness-neutral.
package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kashym/tmux-agent-herd/internal/hooks"
	"github.com/kashym/tmux-agent-herd/internal/jsonord"
)

type kind int

const (
	mergeJSON kind = iota // merge into a {"hooks": {...}} settings file
	ownFile               // write a file herd owns (plugin/extension/hook file)
	aiderYAML             // set notifications-command in ~/.aider.conf.yml
)

// Harness describes one supported agent CLI.
type Harness struct {
	Name     string // used on the command line: herd setup <name>
	Title    string
	Binary   string // command name used to detect an install
	Hooks    string // what the integration uses, for --list
	Note     string // printed after a successful setup
	kind     kind
	template string // file under scripts/hooks/
	config   func() string
	target   func() string
	skills   func() string // "" when the harness has no skills
}

func home() string { h, _ := os.UserHomeDir(); return h }

func envOr(name string, def func() string) func() string {
	return func() string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return def()
	}
}

func xdgConfig() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(home(), ".config")
}

func join(dir func() string, parts ...string) func() string {
	return func() string { return filepath.Join(append([]string{dir()}, parts...)...) }
}

var (
	claudeDir   = envOr("CLAUDE_CONFIG_DIR", func() string { return filepath.Join(home(), ".claude") })
	codexDir    = envOr("CODEX_HOME", func() string { return filepath.Join(home(), ".codex") })
	geminiDir   = func() string { return filepath.Join(home(), ".gemini") }
	copilotDir  = envOr("COPILOT_HOME", func() string { return filepath.Join(home(), ".copilot") })
	opencodeDir = func() string { return filepath.Join(xdgConfig(), "opencode") }
	piDir       = envOr("PI_CODING_AGENT_DIR", func() string { return filepath.Join(home(), ".pi", "agent") })
	aiderConf   = func() string { return filepath.Join(home(), ".aider.conf.yml") }
)

var all = []*Harness{
	{Name: "claude", Title: "Claude Code", Binary: "claude", kind: mergeJSON, template: "claude.json",
		Hooks:  "hooks in settings.json",
		Note:   "Restart running Claude Code sessions to pick up the hooks.",
		config: claudeDir, target: join(claudeDir, "settings.json"), skills: join(claudeDir, "skills")},
	{Name: "codex", Title: "Codex CLI", Binary: "codex", kind: mergeJSON, template: "codex.json",
		Hooks:  "hooks.json",
		Note:   "Codex runs new hooks only after you trust them: open Codex, run /hooks and trust the herd hooks.",
		config: codexDir, target: join(codexDir, "hooks.json"), skills: join(codexDir, "skills")},
	{Name: "gemini", Title: "Gemini CLI", Binary: "gemini", kind: mergeJSON, template: "gemini.json",
		Hooks:  "hooks in settings.json",
		Note:   "Restart Gemini CLI sessions (hooks need Gemini CLI 0.26 or newer).",
		config: geminiDir, target: join(geminiDir, "settings.json"), skills: join(geminiDir, "skills")},
	{Name: "copilot", Title: "GitHub Copilot CLI", Binary: "copilot", kind: ownFile, template: "copilot.json",
		Hooks:  "hooks/herd.json",
		Note:   "Restart running Copilot CLI sessions to pick up the hooks.",
		config: copilotDir, target: join(copilotDir, "hooks", "herd.json"), skills: join(copilotDir, "skills")},
	{Name: "opencode", Title: "OpenCode", Binary: "opencode", kind: ownFile, template: "opencode.js",
		Hooks:  "server plugin",
		Note:   "Restart OpenCode to load the plugin. State is reported for the pane the OpenCode server runs in.",
		config: opencodeDir, target: join(opencodeDir, "plugins", "herd.js"), skills: join(opencodeDir, "skills")},
	{Name: "pi", Title: "pi", Binary: "pi", kind: ownFile, template: "pi.ts",
		Hooks:  "extension",
		Note:   "Restart pi (or run /reload) to load the extension.",
		config: piDir, target: join(piDir, "extensions", "herd.ts"), skills: join(piDir, "skills")},
	{Name: "aider", Title: "aider", Binary: "aider", kind: aiderYAML,
		Hooks:  "notifications-command",
		Note:   "aider only reports when it finishes a reply; working and blocked still come from screen heuristics.",
		config: func() string { return home() }, target: aiderConf},
}

// All returns every supported harness.
func All() []*Harness { return all }

// Names lists the harness names.
func Names() []string {
	var n []string
	for _, h := range all {
		n = append(n, h.Name)
	}
	return n
}

// Get finds a harness by name.
func Get(name string) (*Harness, error) {
	for _, h := range all {
		if h.Name == name {
			return h, nil
		}
	}
	return nil, fmt.Errorf("unknown harness %q (supported: %s)", name, strings.Join(Names(), ", "))
}

// Target is the file setup writes.
func (h *Harness) Target() string { return h.target() }

// SkillDir is where the harness looks for personal skills ("" if none).
func (h *Harness) SkillDir() string {
	if h.skills == nil {
		return ""
	}
	return h.skills()
}

// Installed reports whether the harness's binary is on PATH or its config
// directory exists.
func (h *Harness) Installed() bool {
	if _, err := exec.LookPath(h.Binary); err == nil {
		return true
	}
	if h.kind == aiderYAML {
		_, err := os.Stat(h.target())
		return err == nil
	}
	st, err := os.Stat(h.config())
	return err == nil && st.IsDir()
}

// Plan returns the target file's current content (nil if missing) and the
// content it should have. Equal content means herd is already set up.
func (h *Harness) Plan(pluginDir, hookPath string) (old, next []byte, err error) {
	old, err = os.ReadFile(h.target())
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	exists := err == nil
	if !exists {
		old = nil
	}
	switch h.kind {
	case mergeJSON:
		next, err = h.planMerge(pluginDir, hookPath, old)
	case ownFile:
		next, err = h.planOwnFile(pluginDir, hookPath, old)
	case aiderYAML:
		next, err = planAider(hookPath, old)
	}
	return old, next, err
}

func (h *Harness) readTemplate(pluginDir string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(pluginDir, "scripts", "hooks", h.template))
	if err != nil {
		return nil, fmt.Errorf("hook template for %s not found: %w", h.Name, err)
	}
	return b, nil
}

func (h *Harness) planMerge(pluginDir, hookPath string, old []byte) ([]byte, error) {
	snippet, err := h.readTemplate(pluginDir)
	if err != nil {
		return nil, err
	}
	var doc any
	if len(bytes.TrimSpace(old)) > 0 {
		if doc, err = jsonord.Decode(old); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON (%v); not touching it", h.target(), err)
		}
	}
	merged, changed, err := hooks.MergeHooksJSON(doc, snippet, hookPath)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", h.target(), err)
	}
	if !changed {
		return old, nil
	}
	return jsonord.Encode(merged), nil
}

// Render fills a template's placeholders: __HERD_HOOK__ becomes the
// shell-quoted hook path (escaped for a JSON string), __HERD_HOOK_JSON__ a
// JSON/JS string literal of the path.
func Render(tmpl []byte, hookPath string) []byte {
	lit, _ := json.Marshal(hookPath)
	quoted, _ := json.Marshal(hooks.ShellQuote(hookPath))
	s := strings.ReplaceAll(string(tmpl), "__HERD_HOOK_JSON__", string(lit))
	s = strings.ReplaceAll(s, hooks.Placeholder, string(quoted[1:len(quoted)-1]))
	return []byte(s)
}

func (h *Harness) planOwnFile(pluginDir, hookPath string, old []byte) ([]byte, error) {
	tmpl, err := h.readTemplate(pluginDir)
	if err != nil {
		return nil, err
	}
	next := Render(tmpl, hookPath)
	if old != nil && !bytes.Equal(old, next) && !bytes.Contains(old, []byte("herd-hook")) && !bytes.Contains(old, []byte("tmux-agent-herd")) {
		return nil, fmt.Errorf("%s exists and was not written by herd; move it away first", h.target())
	}
	return next, nil
}

// planAider sets `notifications: true` and a herd notifications-command,
// leaving every other line alone. A notifications-command that is not
// herd's is never replaced.
func planAider(hookPath string, old []byte) ([]byte, error) {
	cmd := hooks.ShellQuote(hookPath) + " --agent aider --source screen done"
	line := "notifications-command: '" + strings.ReplaceAll(cmd, "'", "''") + "'"
	var lines []string
	if len(old) > 0 {
		lines = strings.Split(strings.TrimRight(string(old), "\n"), "\n")
	}
	haveCmd, haveOn := false, false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "notifications-command:"):
			if !strings.Contains(t, "herd-hook") {
				return nil, fmt.Errorf("~/.aider.conf.yml already has a notifications-command; to use herd as well, call %s from it", cmd)
			}
			lines[i], haveCmd = line, true
		case strings.HasPrefix(t, "notifications:"):
			lines[i], haveOn = "notifications: true", true
		}
	}
	if !haveOn || !haveCmd {
		lines = append(lines, "# tmux-agent-herd: report to tmux when aider is waiting for input")
		if !haveOn {
			lines = append(lines, "notifications: true")
		}
		if !haveCmd {
			lines = append(lines, line)
		}
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

// Status describes a harness for `herd setup --list`.
type Status struct {
	Harness    *Harness
	Installed  bool
	Configured bool
	Err        error
}

// Statuses reports every harness, installed ones first.
func Statuses(pluginDir, hookPath string) []Status {
	var out []Status
	for _, h := range all {
		s := Status{Harness: h, Installed: h.Installed()}
		old, next, err := h.Plan(pluginDir, hookPath)
		s.Err = err
		s.Configured = err == nil && old != nil && bytes.Equal(old, next)
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Installed && !out[j].Installed })
	return out
}

// PlanRemove returns the target's current content and what it should be
// with herd removed. remove=true means the whole file is herd's and should
// be deleted. old == nil means there is nothing to do.
func (h *Harness) PlanRemove() (old, next []byte, remove bool, err error) {
	old, err = os.ReadFile(h.target())
	if os.IsNotExist(err) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	switch h.kind {
	case ownFile:
		if !bytes.Contains(old, []byte("herd-hook")) && !bytes.Contains(old, []byte("tmux-agent-herd")) {
			return nil, nil, false, nil // not ours
		}
		return old, nil, true, nil
	case mergeJSON:
		if len(bytes.TrimSpace(old)) == 0 {
			return nil, nil, false, nil
		}
		doc, err := jsonord.Decode(old)
		if err != nil {
			return nil, nil, false, fmt.Errorf("%s is not valid JSON (%v); not touching it", h.target(), err)
		}
		if !hooks.RemoveHooksJSON(doc) {
			return nil, nil, false, nil
		}
		next := jsonord.Encode(doc)
		if string(next) == "{}\n" {
			return old, nil, true, nil // nothing but herd was in it
		}
		return old, next, false, nil
	case aiderYAML:
		next := removeAider(old)
		if bytes.Equal(next, old) {
			return nil, nil, false, nil
		}
		return old, next, false, nil
	}
	return nil, nil, false, nil
}

func removeAider(old []byte) []byte {
	lines := strings.Split(strings.TrimRight(string(old), "\n"), "\n")
	var out []string
	afterMarker := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "# tmux-agent-herd:"):
			afterMarker = true
			continue
		case strings.HasPrefix(t, "notifications-command:") && strings.Contains(t, "herd-hook"):
			continue
		case afterMarker && t == "notifications: true":
			continue // we added it
		}
		afterMarker = false
		out = append(out, l)
	}
	if len(out) == 0 {
		return []byte{}
	}
	return []byte(strings.Join(out, "\n") + "\n")
}
