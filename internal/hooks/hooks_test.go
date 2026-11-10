package hooks

import (
	"testing"

	"github.com/kashym/tmux-agent-herd/internal/state"
)

func TestMap(t *testing.T) {
	cases := []struct {
		name string
		arg  string
		json string
		want Event
		ok   bool
	}{
		// Claude Code
		{"claude-prompt", "working", `{"hook_event_name":"UserPromptSubmit","prompt":"hi"}`, Event{State: state.Working}, true},
		{"claude-pretool", "working", `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`, Event{State: state.Working, Msg: "Bash"}, true},
		{"claude-permission", "blocked", `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"}`,
			Event{State: state.Blocked, Kind: "permission", Msg: "Claude needs your permission to use Bash"}, true},
		{"claude-permission-old", "blocked", `{"hook_event_name":"Notification","message":"Claude needs your permission to use Edit"}`,
			Event{State: state.Blocked, Kind: "permission", Msg: "Claude needs your permission to use Edit"}, true},
		{"claude-idle-prompt-ignored", "blocked", `{"hook_event_name":"Notification","notification_type":"idle_prompt","message":"Claude is waiting for your input"}`, Event{}, false},
		{"claude-idle-old-ignored", "blocked", `{"hook_event_name":"Notification","message":"Claude is waiting for your input"}`, Event{}, false},
		{"claude-stop", "done", `{"hook_event_name":"Stop"}`, Event{State: state.Done}, true},
		{"claude-end", "none", `{"hook_event_name":"SessionEnd"}`, Event{State: state.None}, true},
		// Codex
		{"codex-permission", "blocked", `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf dist","description":null}}`,
			Event{State: state.Blocked, Kind: "permission", Msg: "Bash: rm -rf dist"}, true},
		{"codex-stop", "done", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`, Event{State: state.Done}, true},
		// Gemini CLI
		{"gemini-tool", "working", `{"hook_event_name":"BeforeTool","tool_name":"run_shell_command"}`, Event{State: state.Working, Msg: "run_shell_command"}, true},
		{"gemini-permission", "blocked", `{"hook_event_name":"Notification","notification_type":"ToolPermission","message":"Allow shell command?"}`,
			Event{State: state.Blocked, Kind: "permission", Msg: "Allow shell command?"}, true},
		{"gemini-after-agent-auto", "", `{"hook_event_name":"AfterAgent"}`, Event{State: state.Done}, true},
		// Copilot CLI (camelCase payload)
		{"copilot-tool", "working", `{"toolName":"bash","toolArgs":"{}"}`, Event{State: state.Working}, true},
		{"copilot-permission", "blocked", `{"hook_event_name":"PermissionRequest","toolName":"bash"}`, Event{State: state.Blocked, Kind: "permission", Msg: "bash"}, true},
		{"copilot-auto-stop", "", `{"hook_event_name":"agentStop"}`, Event{State: state.Done}, true},
		// No payload at all (pi, OpenCode, aider, scripts)
		{"no-payload", "working", ``, Event{State: state.Working}, true},
		{"blocked-no-payload", "blocked", ``, Event{State: state.Blocked, Kind: "question"}, true},
		{"end-alias", "end", ``, Event{State: state.None}, true},
		{"bad-state", "sleepy", ``, Event{}, false},
		{"unknown-event", "", `{"hook_event_name":"Whatever"}`, Event{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Map(c.arg, ParsePayload([]byte(c.json)))
			if ok != c.ok || got != c.want {
				t.Fatalf("got %+v,%v want %+v,%v", got, ok, c.want, c.ok)
			}
		})
	}
}
