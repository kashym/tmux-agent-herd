package hooks

import (
	"os"
	"strings"
	"testing"

	"github.com/kashym/tmux-agent-herd/internal/jsonord"
)

func snippet(t *testing.T) []byte {
	b, err := os.ReadFile("../../scripts/hooks/claude.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMergeIntoEmpty(t *testing.T) {
	out, changed, err := MergeHooksJSON(nil, snippet(t), "/opt/herd/scripts/herd-hook")
	if err != nil || !changed {
		t.Fatalf("err=%v changed=%v", err, changed)
	}
	s := string(jsonord.Encode(out))
	for _, ev := range []string{"UserPromptSubmit", "PreToolUse", "Notification", "Stop", "SessionEnd"} {
		if !strings.Contains(s, `"`+ev+`"`) {
			t.Errorf("missing %s", ev)
		}
	}
	if !strings.Contains(s, `"/opt/herd/scripts/herd-hook --agent claude blocked"`) {
		t.Errorf("command not substituted:\n%s", s)
	}
}

func TestMergePreservesAndIsIdempotent(t *testing.T) {
	orig := `{
  "model": "opus",
  "permissions": {
    "allow": ["Bash(ls:*)"]
  },
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "say done"
          }
        ]
      }
    ]
  },
  "zeta": 1.50
}
`
	v, err := jsonord.Decode([]byte(orig))
	if err != nil {
		t.Fatal(err)
	}
	out, changed, err := MergeHooksJSON(v, snippet(t), "/p/with space/herd-hook")
	if err != nil || !changed {
		t.Fatalf("err=%v changed=%v", err, changed)
	}
	s := string(jsonord.Encode(out))
	if !strings.HasPrefix(s, "{\n  \"model\": \"opus\",\n  \"permissions\"") {
		t.Errorf("key order not preserved:\n%s", s)
	}
	if !strings.Contains(s, `"zeta": 1.50`) {
		t.Errorf("number formatting changed:\n%s", s)
	}
	if !strings.Contains(s, `"say done"`) {
		t.Errorf("existing Stop hook lost")
	}
	if !strings.Contains(s, `"'/p/with space/herd-hook' --agent claude done"`) {
		t.Errorf("path not quoted:\n%s", s)
	}
	// Second merge is a no-op.
	v2, _ := jsonord.Decode([]byte(s))
	out2, changed2, err := MergeHooksJSON(v2, snippet(t), "/p/with space/herd-hook")
	if err != nil || changed2 {
		t.Fatalf("second merge changed=%v err=%v", changed2, err)
	}
	if string(jsonord.Encode(out2)) != s {
		t.Error("re-encode not stable")
	}
	// Plugin moved: commands are updated in place, not duplicated.
	v3, _ := jsonord.Decode([]byte(s))
	out3, changed3, _ := MergeHooksJSON(v3, snippet(t), "/new/herd-hook")
	s3 := string(jsonord.Encode(out3))
	if !changed3 || strings.Contains(s3, "with space") || strings.Count(s3, "/new/herd-hook --agent claude done") != 1 {
		t.Errorf("move not handled:\n%s", s3)
	}
}

func TestMergeRejectsNonObject(t *testing.T) {
	v, _ := jsonord.Decode([]byte(`[1,2]`))
	if _, _, err := MergeHooksJSON(v, snippet(t), "/x"); err == nil {
		t.Error("want error")
	}
}
