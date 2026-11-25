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

func TestCursor(t *testing.T) {
	base := `"conversation_id":"c","generation_id":"g","model":"claude-4.5-sonnet","cursor_version":"2.1","workspace_roots":["/w/cs2"],"user_email":null`
	e := one(t, "cursor", `{"hook_event_name":"beforeSubmitPrompt",`+base+`,"prompt":"fix the smoke","attachments":[]}`)
	if e.Kind != event.Prompt || e.PromptText != "fix the smoke" || e.Project != "cs2" || e.Model != "claude-4.5-sonnet" || e.Harness != "cursor" || e.Repo != "github.com/x/cs2" {
		t.Fatalf("%+v", e)
	}
	e = one(t, "cursor", `{"hook_event_name":"afterFileEdit",`+base+`,"file_path":"/w/cs2/a.ts","edits":[{"old_string":"a","new_string":"b\nc"},{"old_string":"","new_string":"d"}]}`)
	if e.Kind != event.Edit || e.Human || e.LinesAdded != 3 || e.LinesRemoved != 1 {
		t.Fatalf("%+v", e)
	}
	if one(t, "cursor", `{"hook_event_name":"afterShellExecution",`+base+`,"command":"ls"}`).Kind != event.ToolCall {
		t.Fatal("shell")
	}
	if one(t, "cursor", `{"hook_event_name":"stop",`+base+`,"status":"completed","loop_count":0}`).Kind != event.Stop {
		t.Fatal("stop")
	}
	if one(t, "cursor", `{"hook_event_name":"sessionStart",`+base+`}`).Kind != event.SessionStart {
		t.Fatal("start")
	}
	if evs, _ := Parse("cursor", []byte(`{"hook_event_name":"beforeReadFile",`+base+`}`), now); evs != nil {
		t.Fatal("ignore")
	}
	if _, err := Parse("cursor", []byte("x"), now); err == nil {
		t.Fatal("bad json")
	}
}

func TestAugment(t *testing.T) {
	e := one(t, "augment", `{"hook_event_name":"PromptSubmit","cwd":"/a/site","conversation_id":"x","user_prompt":"add dark mode","context":{"modelName":"opus"}}`)
	if e.Kind != event.Prompt || e.PromptText != "add dark mode" || e.Project != "site" || e.Model != "opus" {
		t.Fatalf("%+v", e)
	}
	e = one(t, "augment", `{"hook_event_name":"PostToolUse","cwd":"/a/site","tool_name":"Edit","tool_input":{"old_string":"x","new_string":"y\nz"}}`)
	if e.Kind != event.Edit || e.LinesAdded != 2 {
		t.Fatalf("%+v", e)
	}
	if one(t, "augment", `{"hook_event_name":"PostToolUse","cwd":"/a/site","tool_name":"Bash"}`).Kind != event.ToolCall {
		t.Fatal("tool")
	}
	if one(t, "augment", `{"hook_event_name":"Stop","cwd":"/a/site"}`).Kind != event.Stop {
		t.Fatal("stop")
	}
}

// Copilot's PascalCase events (docs.github.com/en/copilot/reference/hooks-configuration)
func TestCopilot(t *testing.T) {
	e := one(t, "copilot", `{"hook_event_name":"UserPromptSubmit","session_id":"s","timestamp":"2026-11-19T10:00:00Z","cwd":"/c/app","prompt":"add tests"}`)
	if e.Kind != event.Prompt || e.Harness != "copilot" || e.Project != "app" || e.PromptText != "add tests" {
		t.Fatalf("%+v", e)
	}
	e = one(t, "copilot", `{"hook_event_name":"PostToolUse","cwd":"/c/app","tool_name":"Edit","tool_input":{"old_string":"a","new_string":"b"},"tool_result":{"result_type":"success"}}`)
	if e.Kind != event.Edit || e.LinesAdded != 1 {
		t.Fatalf("%+v", e)
	}
	if one(t, "copilot", `{"hook_event_name":"Stop","cwd":"/c/app","stop_reason":"end_turn"}`).Kind != event.Stop {
		t.Fatal("stop")
	}
	if one(t, "continue", `{"hook_event_name":"UserPromptSubmit","cwd":"/c/app","prompt":"x"}`).Harness != "continue" {
		t.Fatal("continue")
	}
}

func TestReply(t *testing.T) {
	if Reply("cursor", []byte(`{"hook_event_name":"beforeSubmitPrompt"}`)) != `{"continue":true}` || Reply("cursor", []byte(`{"hook_event_name":"stop"}`)) != `{}` ||
		Reply("cursor", []byte(`garbage`)) != `{}` || Reply("claude-code", nil) != "" {
		t.Fatal("reply")
	}
}

func TestTestRunDetection(t *testing.T) {
	pass := one(t, "claude-code", `{"hook_event_name":"PostToolUse","cwd":"/x/p","tool_name":"Bash","tool_input":{"command":"cd pkg && go  test ./... -race"}}`)
	fail := one(t, "claude-code", `{"hook_event_name":"PostToolUseFailure","cwd":"/x/p","tool_name":"Bash","tool_input":{"command":"cd pkg && go test ./... -race"}}`)
	if pass.Kind != event.TestRun || fail.Kind != event.TestRun {
		t.Fatalf("%+v %+v", pass, fail)
	}
	if !*pass.Pass || *fail.Pass || pass.CmdHash == "" || pass.CmdHash != fail.CmdHash {
		t.Fatalf("fail and pass of the same command must pair: %+v %+v", pass, fail)
	}
	if pass.CmdHash == "" || len(pass.CmdHash) != 12 {
		t.Fatal(pass.CmdHash)
	}
	// non-test shell commands stay plain tool calls; a failing non-test command too
	if one(t, "claude-code", `{"hook_event_name":"PostToolUse","cwd":"/x/p","tool_name":"Bash","tool_input":{"command":"ls -la"}}`).Kind != event.ToolCall {
		t.Fatal("ls is not a test run")
	}
	if one(t, "claude-code", `{"hook_event_name":"PostToolUseFailure","cwd":"/x/p","tool_name":"Bash","tool_input":{"command":"rm nope"}}`).Kind != event.ToolCall {
		t.Fatal("failed non-test command should be a tool call")
	}
	for _, c := range []string{"pytest -x", "npm test", "cargo test --all", "make test", "bun test", "echo go testing"} {
		want := c != "echo go testing"
		if got := testRunEvent(event.Event{}, c, true) != nil; got != want {
			t.Errorf("%q: detected=%v want %v", c, got, want)
		}
	}
	// the Gemini CLI shell tool
	if g := one(t, "gemini-cli", `{"hook_event_name":"AfterTool","cwd":"/x/p","tool_name":"run_shell_command","tool_input":{"command":"pytest"}}`); g.Kind != event.TestRun {
		t.Fatalf("%+v", g)
	}
}

func TestPromptRelevance(t *testing.T) {
	old := RelevanceFor
	defer func() { RelevanceFor = old }()
	RelevanceFor = func(cwd, prompt string) *int { r := 640; return &r }
	e := one(t, "claude-code", `{"hook_event_name":"UserPromptSubmit","cwd":"/x/p","prompt":"fix the syncer retry loop please"}`)
	if e.RelevancePM == nil || *e.RelevancePM != 640 {
		t.Fatalf("%+v", e)
	}
	// real implementation: short prompts are unknown, a repo's own words score
	RelevanceFor = old
	if defaultRelevanceFor(t.TempDir(), "ok") != nil {
		t.Fatal("short prompt should have no relevance")
	}
	dir := t.TempDir()
	for _, c := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x"}} {
		if err := exec.Command("git", append([]string{"-C", dir}, c...)...).Run(); err != nil {
			t.Skip("git unavailable")
		}
	}
	os.WriteFile(filepath.Join(dir, "syncer.go"), []byte("func flushRetryQueue() {}"), 0o644)
	exec.Command("git", "-C", dir, "add", ".").Run()
	r := defaultRelevanceFor(dir, "fix the flushRetryQueue bug in syncer")
	if r == nil || *r < 500 {
		t.Fatalf("relevance %v", r)
	}
	if r := defaultRelevanceFor(dir, "write a poem about autumn leaves falling"); r == nil || *r > 150 {
		t.Fatalf("unrelated prompt scored %v", r)
	}
}

func TestTestRunAcrossHarnesses(t *testing.T) {
	// Copilot / Continue / Augment: lowercase shell tool, outcome in tool_response
	fail := one(t, "copilot", `{"hook_event_name":"PostToolUse","cwd":"/x/p","tool_name":"bash","tool_input":{"command":"pytest -q"},"tool_response":{"exit_code":1}}`)
	pass := one(t, "copilot", `{"hook_event_name":"PostToolUse","cwd":"/x/p","tool_name":"bash","tool_input":{"command":"pytest -q"},"tool_response":{"exit_code":0}}`)
	if fail.Kind != event.TestRun || *fail.Pass || pass.Kind != event.TestRun || !*pass.Pass || fail.CmdHash != pass.CmdHash {
		t.Fatalf("%+v %+v", fail, pass)
	}
	if e := one(t, "claude-code", `{"hook_event_name":"PostToolUse","cwd":"/x/p","tool_name":"Bash","tool_input":{"command":"npm test"},"tool_response":{"interrupted":true}}`); *e.Pass {
		t.Fatal("interrupted run is not a pass")
	}
	// Cursor afterShellExecution has the command at the top level
	c := one(t, "cursor", `{"hook_event_name":"afterShellExecution","workspace_roots":["/x/p"],"command":"cargo test","output":"ok"}`)
	if c.Kind != event.TestRun || !*c.Pass || c.Project != "p" {
		t.Fatalf("%+v", c)
	}
	c = one(t, "augment", `{"hook_event_name":"PostToolUse","cwd":"/x/p","tool_name":"launch-process","tool_input":{"command":"go test ./..."},"exit_code":2}`)
	if c.Kind != event.TestRun || *c.Pass {
		t.Fatalf("%+v", c)
	}
	if e := one(t, "cursor", `{"hook_event_name":"afterShellExecution","workspace_roots":["/x/p"],"command":"ls"}`); e.Kind != event.ToolCall {
		t.Fatalf("%+v", e)
	}
}
