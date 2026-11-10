package harness

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const pluginDir = "../.."

// resolved before tests isolate PATH
var nodeBin, _ = exec.LookPath("node")

func isolate(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	for _, v := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "COPILOT_HOME", "XDG_CONFIG_HOME", "PI_CODING_AGENT_DIR"} {
		t.Setenv(v, "")
	}
	t.Setenv("PATH", "/nonexistent") // nothing "installed"
	return h
}

func apply(t *testing.T, h *Harness, hook string) []byte {
	t.Helper()
	_, next, err := h.Plan(pluginDir, hook)
	if err != nil {
		t.Fatalf("%s: %v", h.Name, err)
	}
	if err := os.MkdirAll(filepath.Dir(h.Target()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Target(), next, 0o644); err != nil {
		t.Fatal(err)
	}
	return next
}

func TestEveryHarnessFromScratchAndIdempotent(t *testing.T) {
	home := isolate(t)
	hook := "/opt/my plugins/herd/scripts/herd-hook"
	for _, h := range All() {
		t.Run(h.Name, func(t *testing.T) {
			if !strings.HasPrefix(h.Target(), home) {
				t.Fatalf("target %s outside HOME", h.Target())
			}
			out := apply(t, h, hook)
			s := string(out)
			if !strings.Contains(s, "herd-hook") || !strings.Contains(s, "--agent "+h.Name) && !strings.Contains(s, `"--agent", "`+h.Name+`"`) {
				t.Errorf("%s output does not call herd-hook for agent %s:\n%s", h.Name, h.Name, s)
			}
			if strings.Contains(s, "__HERD_HOOK") {
				t.Errorf("unrendered placeholder:\n%s", s)
			}
			switch filepath.Ext(h.Target()) {
			case ".json":
				var v any
				if err := json.Unmarshal(out, &v); err != nil {
					t.Errorf("invalid JSON: %v\n%s", err, s)
				}
			case ".js":
				if nodeBin == "" {
					t.Skip("node not installed")
				}
				if o, err := exec.Command(nodeBin, "--check", h.Target()).CombinedOutput(); err != nil {
					t.Errorf("node --check: %v\n%s", err, o)
				}
			}
			old, next, err := h.Plan(pluginDir, hook)
			if err != nil || !bytes.Equal(old, next) {
				t.Errorf("second plan not a no-op (err=%v)", err)
			}
		})
	}
}

func TestOwnFileRefusesForeignFile(t *testing.T) {
	isolate(t)
	h, _ := Get("pi")
	_ = os.MkdirAll(filepath.Dir(h.Target()), 0o755)
	_ = os.WriteFile(h.Target(), []byte("export default function (pi) {}\n"), 0o644)
	if _, _, err := h.Plan(pluginDir, "/x/herd-hook"); err == nil {
		t.Fatal("should refuse to overwrite a file herd did not write")
	}
}

func TestMergeKeepsOtherSettings(t *testing.T) {
	isolate(t)
	for _, name := range []string{"claude", "codex", "gemini"} {
		h, _ := Get(name)
		_ = os.MkdirAll(filepath.Dir(h.Target()), 0o755)
		_ = os.WriteFile(h.Target(), []byte(`{"theme": "dark", "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "notify me"}]}]}}`), 0o600)
		_, next, err := h.Plan(pluginDir, "/x/herd-hook")
		if err != nil {
			t.Fatal(err)
		}
		s := string(next)
		if !strings.Contains(s, `"theme": "dark"`) || !strings.Contains(s, "notify me") {
			t.Errorf("%s lost settings:\n%s", name, s)
		}
	}
}

func TestAider(t *testing.T) {
	isolate(t)
	h, _ := Get("aider")
	_ = os.WriteFile(h.Target(), []byte("model: sonnet\nnotifications: false\n"), 0o644)
	_, next, err := h.Plan(pluginDir, "/x/herd-hook")
	if err != nil {
		t.Fatal(err)
	}
	want := "model: sonnet\nnotifications: true\n# tmux-agent-herd: report to tmux when aider is waiting for input\n" +
		"notifications-command: '/x/herd-hook --agent aider --source screen done'\n"
	if string(next) != want {
		t.Errorf("got:\n%s", next)
	}
	_ = os.WriteFile(h.Target(), []byte("notifications-command: say done\n"), 0o644)
	if _, _, err := h.Plan(pluginDir, "/x/herd-hook"); err == nil {
		t.Error("should not replace a user's own notifications-command")
	}
}

func TestRenderQuoting(t *testing.T) {
	out := string(Render([]byte(`{"bash": "__HERD_HOOK__ --agent x done"} const H = __HERD_HOOK_JSON__;`), `/a b/it's/herd-hook`))
	if !strings.Contains(out, `"bash": "'/a b/it'\\''s/herd-hook' --agent x done"`) {
		t.Errorf("shell quoting in JSON: %s", out)
	}
	if !strings.Contains(out, `const H = "/a b/it's/herd-hook";`) {
		t.Errorf("JS literal: %s", out)
	}
}

func TestSkillDirs(t *testing.T) {
	home := isolate(t)
	for _, h := range All() {
		d := h.SkillDir()
		if h.Name == "aider" {
			if d != "" {
				t.Errorf("aider has no skills")
			}
			continue
		}
		if !strings.HasPrefix(d, home) || filepath.Base(d) != "skills" {
			t.Errorf("%s skill dir %q", h.Name, d)
		}
	}
}

func TestRemoveRoundTrip(t *testing.T) {
	isolate(t)
	orig := map[string]string{
		"claude": "{\n  \"theme\": \"dark\",\n  \"hooks\": {\n    \"Stop\": [\n      {\n        \"hooks\": [\n          {\n            \"type\": \"command\",\n            \"command\": \"notify me\"\n          }\n        ]\n      }\n    ]\n  }\n}\n",
		"codex":  "{\n  \"description\": \"mine\"\n}\n",
		"aider":  "model: sonnet\n",
	}
	for _, h := range All() {
		t.Run(h.Name, func(t *testing.T) {
			if o, ok := orig[h.Name]; ok {
				_ = os.MkdirAll(filepath.Dir(h.Target()), 0o755)
				_ = os.WriteFile(h.Target(), []byte(o), 0o644)
			}
			apply(t, h, "/x/herd-hook")
			old, next, remove, err := h.PlanRemove()
			if err != nil || old == nil {
				t.Fatalf("nothing to remove: err=%v", err)
			}
			if o, ok := orig[h.Name]; ok {
				if remove || string(next) != o {
					t.Fatalf("remove did not restore the original:\n%s\nwant:\n%s", next, o)
				}
				_ = os.WriteFile(h.Target(), next, 0o644)
			} else {
				if !remove {
					t.Fatal("a file holding only herd's config should be deleted")
				}
				_ = os.Remove(h.Target())
			}
			if old, _, _, _ := h.PlanRemove(); old != nil {
				t.Error("second remove should be a no-op")
			}
		})
	}
}
