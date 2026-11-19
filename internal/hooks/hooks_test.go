package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/event"
)

var now = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

func init() {
	ProjectFor = func(cwd string) string { return filepath.Base(cwd) }
	RepoFor = func(cwd string) string { return "github.com/x/" + filepath.Base(cwd) }
}

func one(t *testing.T, h, raw string) event.Event {
	t.Helper()
	evs, err := Parse(h, []byte(raw), now)
	if err != nil || len(evs) != 1 {
		t.Fatalf("%s: %v %v", raw, evs, err)
	}
	if err := evs[0].Validate(); err != nil {
		t.Fatal(err)
	}
	return evs[0]
}

func TestClaude(t *testing.T) {
	e := one(t, "claude-code", `{"hook_event_name":"UserPromptSubmit","cwd":"/x/cs2","prompt":"fix recoil"}`)
	if e.Kind != event.Prompt || e.Project != "cs2" || e.PromptText != "fix recoil" || e.Harness != "claude-code" {
		t.Fatalf("%+v", e)
	}
	e = one(t, "claude-code", `{"hook_event_name":"PostToolUse","cwd":"/x/cs2","tool_name":"Edit","tool_input":{"old_string":"a\nb","new_string":"a\nb\nc"}}`)
	if e.Kind != event.Edit || e.Human || e.LinesAdded != 3 || e.LinesRemoved != 2 {
		t.Fatalf("%+v", e)
	}
	e = one(t, "claude-code", `{"hook_event_name":"PostToolUse","cwd":"/x/p","tool_name":"MultiEdit","tool_input":{"edits":[{"old_string":"a","new_string":"b\nc"}]}}`)
	if e.LinesAdded != 2 || e.LinesRemoved != 1 {
		t.Fatalf("%+v", e)
	}
	if one(t, "claude-code", `{"hook_event_name":"PostToolUse","cwd":"/x/p","tool_name":"Bash"}`).Kind != event.ToolCall {
		t.Fatal("bash")
	}
	if one(t, "claude-code", `{"hook_event_name":"Stop","cwd":"/x/p"}`).Kind != event.Stop {
		t.Fatal("stop")
	}
	if one(t, "claude-code", `{"hook_event_name":"SessionStart","cwd":"/x/p"}`).Kind != event.SessionStart {
		t.Fatal("start")
	}
	if evs, _ := Parse("claude-code", []byte(`{"hook_event_name":"Notification"}`), now); evs != nil {
		t.Fatal("should ignore")
	}
}

func TestGemini(t *testing.T) {
	if e := one(t, "gemini-cli", `{"hook_event_name":"BeforeAgent","cwd":"/g/app","prompt":"hi"}`); e.Kind != event.Prompt || e.Harness != "gemini-cli" {
		t.Fatalf("%+v", e)
	}
	if e := one(t, "gemini-cli", `{"hook_event_name":"AfterTool","cwd":"/g/app","tool_name":"write_file","tool_input":{"content":"x\ny"}}`); e.LinesAdded != 2 {
		t.Fatalf("%+v", e)
	}
}

func TestCodex(t *testing.T) {
	evs, err := Parse("codex", []byte(`{"type":"agent-turn-complete","cwd":"/c/game","input-messages":["a","b"]}`), now)
	if err != nil || len(evs) != 3 || evs[0].Kind != event.Prompt || evs[2].Kind != event.Stop || evs[0].Project != "game" {
		t.Fatalf("%+v %v", evs, err)
	}
	if evs, _ := Parse("codex", []byte(`{"type":"other"}`), now); evs != nil {
		t.Fatal("should ignore")
	}
}

func TestErrors(t *testing.T) {
	for _, h := range []string{"claude-code", "codex"} {
		if _, err := Parse(h, []byte("nope"), now); err == nil {
			t.Error(h)
		}
	}
	if _, err := Parse("vim", []byte("{}"), now); err == nil {
		t.Error("unknown harness")
	}
}

func TestProjectForGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	saved := ProjectFor
	defer func() { ProjectFor = saved }()
	ProjectFor = defaultProjectFor
	root := filepath.Join(t.TempDir(), "myrepo")
	sub := filepath.Join(root, "src", "deep")
	os.MkdirAll(sub, 0o755)
	if err := exec.Command("git", "init", "-q", root).Run(); err != nil {
		t.Skip(err)
	}
	if got := ProjectFor(sub); got != "myrepo" {
		t.Fatalf("got %q", got)
	}
	plain := filepath.Join(t.TempDir(), "plain")
	os.MkdirAll(plain, 0o755)
	if got := ProjectFor(plain); got != "plain" {
		t.Fatalf("got %q", got)
	}
	if ProjectFor("") != "" {
		t.Fatal("empty")
	}
}

func TestNormalizeRepo(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:Owner/Name.git\n":      "github.com/owner/name",
		"https://github.com/owner/name":        "github.com/owner/name",
		"https://user:tok@github.com/o/n.git/": "github.com/o/n",
		"ssh://git@gitlab.com/o/n.git":         "gitlab.com/o/n",
	} {
		if got := NormalizeRepo(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestRepoAttached(t *testing.T) {
	if e := one(t, "claude-code", `{"hook_event_name":"Stop","cwd":"/x/game"}`); e.Repo != "github.com/x/game" {
		t.Fatal(e.Repo)
	}
}

func TestRepoForGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip()
	}
	root := t.TempDir()
	if exec.Command("git", "init", "-q", root).Run() != nil {
		t.Skip()
	}
	if defaultRepoFor(root) != "" || defaultRepoFor("") != "" {
		t.Fatal("no remote should be empty")
	}
	exec.Command("git", "-C", root, "remote", "add", "origin", "git@github.com:IzieStratt/vibetime.git").Run()
	if got := defaultRepoFor(root); got != "github.com/iziestratt/vibetime" {
		t.Fatal(got)
	}
}
