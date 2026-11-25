// Package hooks converts harness hook payloads into events.
package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/IzieStratt/vibetime/internal/event"
	"github.com/IzieStratt/vibetime/internal/substance"
)

// RelevanceFor scores how much of prompt's vocabulary already exists in cwd's project files
// (nil when unknown). It runs inside a hook, so it gets a small time budget. Stubbed in tests.
var RelevanceFor = defaultRelevanceFor

func defaultRelevanceFor(cwd, prompt string) *int {
	if len(strings.TrimSpace(prompt)) < substance.TrivialChars {
		return nil // too short to matter: length alone already marks it low effort
	}
	v := substance.ProjectVocab(cwd, 150*time.Millisecond)
	if v == nil {
		return nil
	}
	r := substance.Relevance(prompt, v)
	return &r
}

var testCmd = regexp.MustCompile(`(?:^|[\s;&|(])(?:go test|cargo (?:test|nextest)|npm (?:run )?test|npm t|yarn test|pnpm (?:run )?test|bun test|npx (?:jest|vitest)|jest|vitest|pytest|python3? -m (?:pytest|unittest)|tox|make (?:test|check)|mvn (?:\S+ )*test|gradlew? (?:\S+ )*test|dotnet test|rspec|bundle exec rspec|phpunit|mix test|swift test|deno test|zig build test|ctest)(?:\s|$)`)

// testRunEvent returns a TestRun event if command looks like a test command. The command
// itself is not recorded, only a short hash so a fail and a later pass can be paired.
func testRunEvent(base event.Event, command string, pass bool) []event.Event {
	command = strings.Join(strings.Fields(command), " ")
	if command == "" || !testCmd.MatchString(command) {
		return nil
	}
	sum := sha256.Sum256([]byte(command))
	base.Kind, base.CmdHash, base.Pass = event.TestRun, hex.EncodeToString(sum[:6]), &pass
	return []event.Event{base}
}

// ProjectFor names the project for a working directory: the git top-level folder name,
// falling back to the directory's own name.
var ProjectFor = defaultProjectFor

func defaultProjectFor(cwd string) string {
	if cwd == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err == nil {
		if top := strings.TrimSpace(string(out)); top != "" {
			return filepath.Base(top)
		}
	}
	return filepath.Base(filepath.Clean(cwd))
}

// claudeLike is the shared shape of Claude Code and Gemini CLI hook input.
type claudeLike struct {
	Event     string          `json:"hook_event_name"`
	Cwd       string          `json:"cwd"`
	Prompt    string          `json:"prompt"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	// ToolResponse is Claude Code's PostToolUse result; others use tool_output.
	ToolResponse json.RawMessage `json:"tool_response"`
	ToolOutput   json.RawMessage `json:"tool_output"`
}

type shellInput struct {
	Command string `json:"command"`
}

// shellTool reports whether a harness tool name is its shell tool (Claude Code "Bash",
// Gemini "run_shell_command", Copilot/Continue/Augment "bash"/"shell"/"execute_bash"...).
func shellTool(name string) bool {
	switch strings.ToLower(name) {
	case "bash", "shell", "run_shell_command", "execute_bash", "run_command", "launch-process", "terminal":
		return true
	}
	return false
}

// failed reports whether a tool response says the command did not succeed. Harnesses that
// only fire PostToolUse on success send nothing, which counts as a pass.
func failed(resp json.RawMessage) bool {
	var r struct {
		IsError     bool  `json:"is_error"`
		Interrupted bool  `json:"interrupted"`
		ExitCode    *int  `json:"exit_code"`
		ExitCamel   *int  `json:"exitCode"`
		Code        *int  `json:"code"`
		Success     *bool `json:"success"`
	}
	if len(resp) == 0 || json.Unmarshal(resp, &r) != nil {
		return false
	}
	for _, c := range []*int{r.ExitCode, r.ExitCamel, r.Code} {
		if c != nil && *c != 0 {
			return true
		}
	}
	return r.IsError || r.Interrupted || (r.Success != nil && !*r.Success)
}

type editInput struct {
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
	Content   string `json:"content"`
	Edits     []struct {
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	} `json:"edits"`
}

func lines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// Claude parses a Claude Code hook (stdin) payload. harness lets Gemini CLI reuse it.
func Claude(harness string, raw []byte, now time.Time) ([]event.Event, error) {
	var in claudeLike
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	base := event.Event{Time: now, Project: ProjectFor(in.Cwd), Repo: RepoFor(in.Cwd), Harness: harness, Cwd: in.Cwd}
	switch in.Event {
	case "SessionStart":
		base.Kind = event.SessionStart
	case "UserPromptSubmit", "BeforeAgent":
		base.Kind, base.PromptText = event.Prompt, in.Prompt
		base.RelevancePM = RelevanceFor(in.Cwd, in.Prompt)
	case "PostToolUseFailure":
		// Claude Code fires this instead of PostToolUse when a tool errors; a failed test
		// command is the first half of a fail→pass loop. Other failures are still agent work.
		base.Kind = event.ToolCall
		if shellTool(in.ToolName) {
			var si shellInput
			json.Unmarshal(in.ToolInput, &si)
			if t := testRunEvent(base, si.Command, false); t != nil {
				return t, nil
			}
		}
	case "PostToolUse", "AfterTool":
		base.Kind = event.ToolCall
		if shellTool(in.ToolName) {
			var si shellInput
			json.Unmarshal(in.ToolInput, &si)
			resp := in.ToolResponse
			if len(resp) == 0 {
				resp = in.ToolOutput
			}
			if t := testRunEvent(base, si.Command, !failed(resp)); t != nil {
				return t, nil
			}
		}
		switch in.ToolName {
		case "Edit", "Write", "MultiEdit", "write_file", "replace":
			var ei editInput
			json.Unmarshal(in.ToolInput, &ei)
			base.Kind = event.Edit
			base.LinesAdded, base.LinesRemoved = lines(ei.NewString)+lines(ei.Content), lines(ei.OldString)
			for _, e := range ei.Edits {
				base.LinesAdded += lines(e.NewString)
				base.LinesRemoved += lines(e.OldString)
			}
		}
	case "Stop", "AfterAgent", "SessionEnd":
		base.Kind = event.Stop
	default:
		return nil, nil // ignored hook
	}
	return []event.Event{base}, nil
}

// Codex parses the JSON argument Codex CLI passes to its `notify` program.
func Codex(raw []byte, now time.Time) ([]event.Event, error) {
	var in struct {
		Type   string   `json:"type"`
		Cwd    string   `json:"cwd"`
		Inputs []string `json:"input-messages"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	if in.Type != "agent-turn-complete" {
		return nil, nil
	}
	p, repo := ProjectFor(in.Cwd), RepoFor(in.Cwd)
	var out []event.Event
	for _, m := range in.Inputs {
		out = append(out, event.Event{Time: now, Kind: event.Prompt, Project: p, Repo: repo, Harness: "codex", PromptText: m, RelevancePM: RelevanceFor(in.Cwd, m), Cwd: in.Cwd})
	}
	return append(out, event.Event{Time: now, Kind: event.Stop, Project: p, Repo: repo, Harness: "codex", Cwd: in.Cwd}), nil
}

// Parse dispatches by harness name.
func Parse(harness string, raw []byte, now time.Time) ([]event.Event, error) {
	switch harness {
	case "claude-code", "gemini-cli", "copilot", "continue":
		// Copilot's PascalCase hook events and Continue's CLI hooks use Claude Code's payload.
		return Claude(harness, raw, now)
	case "cursor", "augment":
		return Cursor(harness, raw, now)
	case "codex":
		return Codex(raw, now)
	}
	return nil, fmt.Errorf("unknown harness %q", harness)
}

// RepoFor returns the normalized origin remote for cwd ("" if none), e.g.
// "git@github.com:Owner/Name.git" -> "github.com/owner/name".
var RepoFor = defaultRepoFor

func defaultRepoFor(cwd string) string {
	if cwd == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", cwd, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return ""
	}
	return NormalizeRepo(string(out))
}

// NormalizeRepo canonicalizes a git remote URL to host/owner/name in lower case.
func NormalizeRepo(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	for _, p := range []string{"https://", "http://", "ssh://", "git://"} {
		u = strings.TrimPrefix(u, p)
	}
	if i := strings.Index(u, "@"); i >= 0 {
		u = u[i+1:]
	}
	u = strings.Replace(u, ":", "/", 1)
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	return u
}

// cursorLike covers Cursor (cursor.com/docs/hooks) and Augment (docs.augmentcode.com/cli/hooks),
// which share event names via Augment's Cursor aliases but name some fields differently.
type cursorLike struct {
	Event          string   `json:"hook_event_name"`
	Model          string   `json:"model"`
	WorkspaceRoots []string `json:"workspace_roots"`
	Cwd            string   `json:"cwd"`
	Prompt         string   `json:"prompt"`      // Cursor
	UserPrompt     string   `json:"user_prompt"` // Augment
	Edits          []struct {
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	} `json:"edits"`
	ToolName     string          `json:"tool_name"`
	ToolInput    json.RawMessage `json:"tool_input"`
	ToolResponse json.RawMessage `json:"tool_response"`
	ToolOutput   json.RawMessage `json:"tool_output"`
	Command      string          `json:"command"` // Cursor afterShellExecution
	ExitCode     *int            `json:"exit_code"`
	Context      struct {
		ModelName string `json:"modelName"`
	} `json:"context"`
}

// Cursor parses Cursor and Augment hook payloads (both send hook_event_name).
func Cursor(harness string, raw []byte, now time.Time) ([]event.Event, error) {
	var in cursorLike
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	dir := in.Cwd
	if len(in.WorkspaceRoots) > 0 {
		dir = in.WorkspaceRoots[0]
	}
	model := in.Model
	if model == "" {
		model = in.Context.ModelName
	}
	base := ev(now, harness, dir, model)
	switch strings.ToLower(in.Event) {
	case "sessionstart":
		base.Kind = event.SessionStart
	case "beforesubmitprompt", "promptsubmit", "userpromptsubmit":
		base.Kind, base.PromptText = event.Prompt, in.Prompt
		if base.PromptText == "" {
			base.PromptText = in.UserPrompt
		}
		base.RelevancePM = RelevanceFor(dir, base.PromptText)
	case "afterfileedit":
		base.Kind = event.Edit
		for _, e := range in.Edits {
			base.LinesAdded += lines(e.NewString)
			base.LinesRemoved += lines(e.OldString)
		}
	case "posttooluse", "aftershellexecution", "aftermcpexecution":
		base.Kind = event.ToolCall
		cmd, pass := in.Command, true
		if shellTool(in.ToolName) {
			var si shellInput
			json.Unmarshal(in.ToolInput, &si)
			cmd = si.Command
		}
		resp := in.ToolResponse
		if len(resp) == 0 {
			resp = in.ToolOutput
		}
		if failed(resp) || (in.ExitCode != nil && *in.ExitCode != 0) {
			pass = false
		}
		if t := testRunEvent(base, cmd, pass); t != nil {
			return t, nil
		}
		if strings.EqualFold(in.ToolName, "edit") || strings.EqualFold(in.ToolName, "write") {
			var ei editInput
			json.Unmarshal(in.ToolInput, &ei)
			base.Kind = event.Edit
			base.LinesAdded, base.LinesRemoved = lines(ei.NewString)+lines(ei.Content), lines(ei.OldString)
		}
	case "stop", "sessionend":
		base.Kind = event.Stop
	default:
		return nil, nil
	}
	return []event.Event{base}, nil
}

func ev(now time.Time, harness, dir, model string) event.Event {
	return event.Event{Time: now, Project: ProjectFor(dir), Repo: RepoFor(dir), Harness: harness, Model: model, Cwd: dir}
}

// Reply is what the hook must print on stdout for harnesses that read a JSON answer.
// Tracking never blocks anything.
func Reply(harness string, raw []byte) string {
	if harness != "cursor" {
		return ""
	}
	var in struct {
		Event string `json:"hook_event_name"`
	}
	json.Unmarshal(raw, &in)
	if in.Event == "beforeSubmitPrompt" {
		return `{"continue":true}`
	}
	return `{}`
}
