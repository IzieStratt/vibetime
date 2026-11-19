// Package hooks converts harness hook payloads into events.
package hooks

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/IzieStratt/vibetime/internal/event"
)

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
	base := event.Event{Time: now, Project: ProjectFor(in.Cwd), Repo: RepoFor(in.Cwd), Harness: harness}
	switch in.Event {
	case "SessionStart":
		base.Kind = event.SessionStart
	case "UserPromptSubmit", "BeforeAgent":
		base.Kind, base.PromptText = event.Prompt, in.Prompt
	case "PostToolUse", "AfterTool":
		base.Kind = event.ToolCall
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
		out = append(out, event.Event{Time: now, Kind: event.Prompt, Project: p, Repo: repo, Harness: "codex", PromptText: m})
	}
	return append(out, event.Event{Time: now, Kind: event.Stop, Project: p, Repo: repo, Harness: "codex"}), nil
}

// Parse dispatches by harness name.
func Parse(harness string, raw []byte, now time.Time) ([]event.Event, error) {
	switch harness {
	case "claude-code", "gemini-cli":
		return Claude(harness, raw, now)
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
