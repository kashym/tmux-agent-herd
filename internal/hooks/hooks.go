// Package hooks maps agent hook invocations from any harness (Claude Code,
// Codex, Gemini CLI, Copilot CLI, OpenCode, pi, aider, ...) to herd state
// events. Adapters normally pass the state explicitly
// (`herd-hook --agent codex working`); the stdin payload, when present, only
// adds a message and refines blocked prompts.
package hooks

import (
	"encoding/json"
	"strings"

	"github.com/kashym/tmux-agent-herd/internal/state"
)

// Payload is the union of the hook stdin fields we read. Harnesses that
// send other shapes (or nothing) are fine: missing fields stay empty.
type Payload struct {
	HookEventName    string `json:"hook_event_name"` // Claude, Codex, Gemini, Copilot (VS Code format)
	NotificationType string `json:"notification_type"`
	Message          string `json:"message"`
	Title            string `json:"title"`
	ToolName         string `json:"tool_name"`
	ToolNameCamel    string `json:"toolName"` // Copilot CLI
	ToolInput        struct {
		Command     string `json:"command"`
		Description string `json:"description"`
	} `json:"tool_input"`
}

// ParsePayload decodes hook stdin; malformed input yields an empty payload.
func ParsePayload(data []byte) Payload {
	var p Payload
	_ = json.Unmarshal(data, &p)
	return p
}

func (p Payload) tool() string {
	if p.ToolName != "" {
		return p.ToolName
	}
	return p.ToolNameCamel
}

// Event is the result of mapping a hook call.
type Event struct {
	State state.State
	Kind  string
	Msg   string
}

// eventStates maps hook event names from every supported harness to a
// state, for adapters that call herd-hook without an explicit state.
var eventStates = map[string]state.State{
	// Claude Code / Codex
	"UserPromptSubmit": state.Working, "PreToolUse": state.Working, "PostToolUse": state.Working,
	"PermissionRequest": state.Blocked, "Notification": state.Blocked, "Stop": state.Done,
	"SessionStart": state.Idle, "SessionEnd": state.None,
	// Gemini CLI
	"BeforeAgent": state.Working, "BeforeTool": state.Working, "AfterTool": state.Working, "AfterAgent": state.Done,
	// Copilot CLI (camelCase names)
	"userPromptSubmitted": state.Working, "preToolUse": state.Working, "postToolUse": state.Working,
	"permissionRequest": state.Blocked, "agentStop": state.Done, "errorOccurred": state.Error,
	"sessionStart": state.Idle, "sessionEnd": state.None,
}

var toolEvents = map[string]bool{"PreToolUse": true, "BeforeTool": true, "preToolUse": true}
var permissionEvents = map[string]bool{"PermissionRequest": true, "permissionRequest": true}

// Notification types that are not a block: Claude's "waiting for your
// input" after a finished turn, and auth confirmations.
var ignoredNotifications = map[string]bool{"idle_prompt": true, "auth_success": true}

// Map turns `herd-hook <arg>` plus its stdin payload into an event.
// arg may be a state name, "end"/"clear" (= none), or empty to derive the
// state from the payload's hook_event_name. ok is false when the call
// should be ignored.
func Map(arg string, p Payload) (Event, bool) {
	event := p.HookEventName
	var st state.State
	switch a := strings.ToLower(arg); a {
	case "", "auto":
		s, known := eventStates[event]
		if !known {
			return Event{}, false
		}
		st = s
	case "end", "clear":
		st = state.None
	default:
		if !state.Valid(a) {
			return Event{}, false
		}
		st = state.State(a)
	}

	ev := Event{State: st}
	switch st {
	case state.Working:
		if toolEvents[event] {
			ev.Msg = p.tool()
		}
	case state.Blocked:
		switch {
		case permissionEvents[event]:
			ev.Kind = "permission"
			ev.Msg = p.tool()
			if c := firstNonEmpty(p.ToolInput.Command, p.ToolInput.Description); c != "" {
				ev.Msg = strings.TrimPrefix(ev.Msg+": "+c, ": ")
			}
		case p.NotificationType != "":
			if ignoredNotifications[p.NotificationType] {
				return Event{}, false
			}
			ev.Msg = firstNonEmpty(p.Message, p.Title)
			switch p.NotificationType {
			case "permission_prompt", "ToolPermission":
				ev.Kind = "permission"
			case "elicitation_dialog":
				ev.Kind = "question"
			default:
				ev.Kind = kindFromMessage(ev.Msg)
			}
		default:
			ev.Msg = firstNonEmpty(p.Message, p.Title)
			if strings.Contains(strings.ToLower(ev.Msg), "waiting for your input") {
				return Event{}, false
			}
			ev.Kind = kindFromMessage(ev.Msg)
		}
	}
	return ev, true
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func kindFromMessage(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "permission") || strings.Contains(m, "approve") || strings.Contains(m, "allow"):
		return "permission"
	case strings.Contains(m, "login") || strings.Contains(m, "auth"):
		return "auth"
	}
	return "question"
}
