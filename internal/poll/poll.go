// Package poll classifies captured pane screens into agent states using
// per-agent pattern files (patterns/<agent>.toml).
package poll

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/kashym/tmux-agent-herd/internal/state"
	"github.com/kashym/tmux-agent-herd/patterns"
)

// Rule is one pattern rule. Rules are tried in file order; first match wins.
type Rule struct {
	State string `toml:"state"`
	Kind  string `toml:"kind"`
	Match string `toml:"match"`
	// Msg is an optional regex; its first capture group (or whole match)
	// becomes the message. Without it the matching line is used.
	Msg string `toml:"msg"`

	match, msg *regexp.Regexp
}

// Pattern is one agent's pattern file.
type Pattern struct {
	Agent   string `toml:"agent"`
	Lines   int    `toml:"lines"`
	Default string `toml:"default"`
	Rules   []Rule `toml:"rules"`
}

// Set maps agent name to its pattern.
type Set map[string]*Pattern

// Parse parses and compiles one pattern file.
func Parse(name string, data []byte) (*Pattern, error) {
	var p Pattern
	if _, err := toml.Decode(string(data), &p); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if p.Agent == "" {
		p.Agent = strings.TrimSuffix(filepath.Base(name), ".toml")
	}
	if p.Lines <= 0 {
		p.Lines = 40
	}
	if p.Default == "" {
		p.Default = string(state.Idle)
	}
	for i := range p.Rules {
		r := &p.Rules[i]
		if !state.Valid(r.State) {
			return nil, fmt.Errorf("%s: rule %d: bad state %q", name, i, r.State)
		}
		var err error
		if r.match, err = regexp.Compile(r.Match); err != nil {
			return nil, fmt.Errorf("%s: rule %d: %w", name, i, err)
		}
		if r.Msg != "" {
			if r.msg, err = regexp.Compile(r.Msg); err != nil {
				return nil, fmt.Errorf("%s: rule %d msg: %w", name, i, err)
			}
		}
	}
	return &p, nil
}

// Load reads the built-in patterns, then overrides from each dir in order
// (later dirs win per agent). Missing dirs are ignored.
func Load(dirs ...string) (Set, error) {
	set := Set{}
	add := func(fsys fs.FS, label string) error {
		names, _ := fs.Glob(fsys, "*.toml")
		for _, n := range names {
			data, err := fs.ReadFile(fsys, n)
			if err != nil {
				return err
			}
			p, err := Parse(label+"/"+n, data)
			if err != nil {
				return err
			}
			set[p.Agent] = p
		}
		return nil
	}
	if err := add(patterns.FS, "builtin"); err != nil {
		return nil, err
	}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			continue
		}
		if err := add(os.DirFS(d), d); err != nil {
			return nil, err
		}
	}
	return set, nil
}

// Tail returns the last n non-trailing-blank lines of a capture.
func Tail(screen string, n int) string {
	lines := strings.Split(strings.TrimRight(screen, "\n \t"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Classify returns the raw state of a captured screen for agent. ok is false
// when no pattern exists for the agent.
func (s Set) Classify(agent, screen string) (state.Report, bool) {
	p := s[agent]
	if p == nil {
		return state.Report{}, false
	}
	return p.Classify(screen), true
}

// Classify applies the pattern's rules to the screen tail.
func (p *Pattern) Classify(screen string) state.Report {
	text := Tail(screen, p.Lines)
	for _, r := range p.Rules {
		loc := r.match.FindStringIndex(text)
		if loc == nil {
			continue
		}
		rep := state.Report{State: state.State(r.State), Kind: r.Kind}
		if r.msg != nil {
			if m := r.msg.FindStringSubmatch(text); m != nil {
				rep.Msg = m[len(m)-1]
				if len(m) > 1 {
					rep.Msg = m[1]
				}
			}
		} else if rep.State == state.Blocked {
			rep.Msg = lineAt(text, max(loc[0], loc[1]-1))
		}
		rep.Msg = strings.TrimSpace(strings.Trim(rep.Msg, " │╭╮╰╯─|"))
		return rep
	}
	return state.Report{State: state.State(p.Default)}
}

func lineAt(text string, off int) string {
	start := strings.LastIndexByte(text[:off], '\n') + 1
	end := strings.IndexByte(text[off:], '\n')
	if end < 0 {
		return text[start:]
	}
	return text[start : off+end]
}
