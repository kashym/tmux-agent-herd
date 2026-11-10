package tmux

import "testing"

func TestAgentInArgs(t *testing.T) {
	agents := map[string]bool{"claude": true, "gemini": true, "aider": true, "codex": true}
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"/bin/bash", "/tmp/fake/claude"}, "claude"},
		{[]string{"node", "/usr/lib/node_modules/@google/gemini-cli/dist/index.js"}, "gemini"},
		{[]string{"/usr/bin/python3", "/home/u/.local/bin/aider", "--model", "x"}, "aider"},
		{[]string{"node", "--no-warnings", "/opt/codex/bin/codex.js"}, "codex"},
		{[]string{"node", "server.js"}, ""},
		{[]string{"bash"}, ""},
	}
	for _, c := range cases {
		if got := AgentInArgs(c.argv, agents); got != c.want {
			t.Errorf("%q: got %q want %q", c.argv, got, c.want)
		}
	}
}
