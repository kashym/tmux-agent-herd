package poll

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixtures live in test/fixtures/<agent>/<state>[.<kind>]-<desc>.txt.
func TestFixtures(t *testing.T) {
	set, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("../../test/fixtures/*/*.txt")
	if len(files) == 0 {
		t.Fatal("no fixtures found")
	}
	for _, f := range files {
		agent := filepath.Base(filepath.Dir(f))
		name := strings.TrimSuffix(filepath.Base(f), ".txt")
		want, _, _ := strings.Cut(name, "-")
		wantState, wantKind, _ := strings.Cut(want, ".")
		t.Run(agent+"/"+name, func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := set.Classify(agent, string(data))
			if !ok {
				t.Fatalf("no pattern for %s", agent)
			}
			if string(got.State) != wantState || got.Kind != wantKind {
				t.Fatalf("got %s/%s (msg %q), want %s/%s", got.State, got.Kind, got.Msg, wantState, wantKind)
			}
			if got.State == "blocked" && got.Msg == "" {
				t.Errorf("blocked should carry a message")
			}
		})
	}
}

func TestBlockedMessage(t *testing.T) {
	set, _ := Load()
	data, _ := os.ReadFile("../../test/fixtures/claude/blocked.permission-edit.txt")
	got, _ := set.Classify("claude", string(data))
	if got.Msg != "Do you want to make this edit to main.go?" {
		t.Errorf("msg = %q", got.Msg)
	}
}

func TestOverrideDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "claude.toml"), []byte(`
agent = "claude"
[[rules]]
state = "error"
match = 'BOOM'
`), 0o644)
	os.WriteFile(filepath.Join(dir, "mytool.toml"), []byte(`
[[rules]]
state = "working"
match = 'busy'
`), 0o644)
	set, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := set.Classify("claude", "x\nBOOM\n"); r.State != "error" {
		t.Errorf("override not applied: %+v", r)
	}
	if r, ok := set.Classify("mytool", "busy"); !ok || r.State != "working" {
		t.Errorf("agent name from filename: %+v %v", r, ok)
	}
	if _, ok := set.Classify("codex", ""); !ok {
		t.Error("builtin codex should remain")
	}
}

func TestBadPattern(t *testing.T) {
	if _, err := Parse("x.toml", []byte("[[rules]]\nstate='sleepy'\nmatch='a'\n")); err == nil {
		t.Error("want error for bad state")
	}
	if _, err := Parse("x.toml", []byte("[[rules]]\nstate='idle'\nmatch='('\n")); err == nil {
		t.Error("want error for bad regex")
	}
}

func BenchmarkClassify(b *testing.B) {
	set, _ := Load()
	data, _ := os.ReadFile("../../test/fixtures/claude/working-thinking.txt")
	s := strings.Repeat("filler line of transcript text\n", 60) + string(data)
	for i := 0; i < b.N; i++ {
		set.Classify("claude", s)
	}
}
