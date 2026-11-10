package hooks

import (
	"fmt"
	"strings"

	"github.com/kashym/tmux-agent-herd/internal/jsonord"
)

// Placeholder in scripts/hooks/* replaced by the (shell-quoted) herd-hook path.
const Placeholder = "__HERD_HOOK__"

// ShellQuote quotes a path for a shell command line if needed.
func ShellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r == '/' || r == '.' || r == '-' || r == '_' || r == '~' || r == '+' || r == ':' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// MergeHooksJSON merges a hook snippet into a settings document that uses the
// {"hooks": {"<Event>": [{"matcher"?, "hooks": [{"command": ...}]}]}} shape
// (Claude Code settings.json, Codex hooks.json, Gemini CLI settings.json).
// settings may be nil (no file yet). Existing herd-hook entries are updated
// in place (e.g. if the plugin moved); everything else is left untouched.
// Returns the new document and whether anything changed.
func MergeHooksJSON(settings any, snippet []byte, hookPath string) (any, bool, error) {
	snipText := strings.ReplaceAll(string(snippet), Placeholder, strings.ReplaceAll(ShellQuote(hookPath), `\`, `\\`))
	snipV, err := jsonord.Decode([]byte(snipText))
	if err != nil {
		return nil, false, fmt.Errorf("snippet: %w", err)
	}
	snip, ok := snipV.(*jsonord.Object)
	if !ok {
		return nil, false, fmt.Errorf("snippet is not an object")
	}
	snipHooks, _ := snip.M["hooks"].(*jsonord.Object)
	if snipHooks == nil {
		return nil, false, fmt.Errorf("snippet has no hooks")
	}

	root, _ := settings.(*jsonord.Object)
	if settings != nil && root == nil {
		return nil, false, fmt.Errorf("settings file is not a JSON object")
	}
	if root == nil {
		root = jsonord.NewObject()
	}
	hooksV, _ := root.Get("hooks")
	hooksObj, _ := hooksV.(*jsonord.Object)
	if hooksV != nil && hooksObj == nil {
		return nil, false, fmt.Errorf(`"hooks" in the settings file is not an object`)
	}
	changed := false
	if hooksObj == nil {
		hooksObj = jsonord.NewObject()
		root.Set("hooks", hooksObj)
		changed = true
	}

	for _, event := range snipHooks.Keys {
		want := snipHooks.M[event].([]any)
		wantCmd := firstCommand(want)
		cur, _ := hooksObj.M[event].([]any)
		found := false
		for _, g := range cur {
			for _, h := range groupHooks(g) {
				cmd, _ := h.M["command"].(string)
				if strings.Contains(cmd, "herd-hook") {
					found = true
					if cmd != wantCmd {
						h.Set("command", wantCmd)
						changed = true
					}
				}
			}
		}
		if !found {
			hooksObj.Set(event, append(cur, want...))
			changed = true
		}
	}
	return root, changed, nil
}

func groupHooks(g any) []*jsonord.Object {
	obj, _ := g.(*jsonord.Object)
	if obj == nil {
		return nil
	}
	arr, _ := obj.M["hooks"].([]any)
	var out []*jsonord.Object
	for _, h := range arr {
		if o, ok := h.(*jsonord.Object); ok {
			out = append(out, o)
		}
	}
	return out
}

func firstCommand(groups []any) string {
	for _, g := range groups {
		for _, h := range groupHooks(g) {
			if c, ok := h.M["command"].(string); ok {
				return c
			}
		}
	}
	return ""
}

// RemoveHooksJSON deletes every hook whose command calls herd-hook from a
// {"hooks": {...}} settings document, dropping groups and events that become
// empty. Returns whether anything was removed.
func RemoveHooksJSON(doc any) bool {
	root, _ := doc.(*jsonord.Object)
	if root == nil {
		return false
	}
	hooksObj, _ := root.M["hooks"].(*jsonord.Object)
	if hooksObj == nil {
		return false
	}
	removed := false
	var keepEvents []string
	for _, event := range hooksObj.Keys {
		groups, ok := hooksObj.M[event].([]any)
		if !ok {
			keepEvents = append(keepEvents, event)
			continue
		}
		var keepGroups []any
		for _, g := range groups {
			obj, _ := g.(*jsonord.Object)
			arr, _ := obj.M["hooks"].([]any)
			if obj == nil || arr == nil {
				keepGroups = append(keepGroups, g)
				continue
			}
			var keep []any
			for _, h := range arr {
				if o, ok := h.(*jsonord.Object); ok {
					if c, _ := o.M["command"].(string); strings.Contains(c, "herd-hook") {
						removed = true
						continue
					}
				}
				keep = append(keep, h)
			}
			if len(keep) > 0 {
				obj.M["hooks"] = keep
				keepGroups = append(keepGroups, obj)
			}
		}
		if len(keepGroups) > 0 {
			hooksObj.M[event] = keepGroups
			keepEvents = append(keepEvents, event)
		} else {
			delete(hooksObj.M, event)
		}
	}
	hooksObj.Keys = keepEvents
	if len(hooksObj.Keys) == 0 {
		// Drop the "hooks" key entirely if herd was its only user.
		delete(root.M, "hooks")
		var keys []string
		for _, k := range root.Keys {
			if k != "hooks" {
				keys = append(keys, k)
			}
		}
		root.Keys = keys
	}
	return removed
}
