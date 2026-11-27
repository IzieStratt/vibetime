package hooks

import (
	"testing"
	"time"
)

// Hook events carry their working directory so the agent can apply exclusions and score
// relevance; the agent clears it before storage (see api and store tests).
func TestHookEventsCarryCwd(t *testing.T) {
	RelevanceFor = func(string, string) *int { r := 500; return &r }
	defer func() { RelevanceFor = defaultRelevanceFor }()
	const cwd = "/work/secret-project"
	now := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	evs, err := Claude("claude-code", []byte(`{"hook_event_name":"UserPromptSubmit","cwd":"/work/secret-project","prompt":"fix the flush retry"}`), now)
	if err != nil || len(evs) != 1 {
		t.Fatalf("%v %v", evs, err)
	}
	if evs[0].Cwd != cwd {
		t.Errorf("prompt event cwd = %q, want %q", evs[0].Cwd, cwd)
	}
	stop, _ := Claude("claude-code", []byte(`{"hook_event_name":"Stop","cwd":"/work/secret-project"}`), now)
	if len(stop) != 1 || stop[0].Cwd != cwd {
		t.Errorf("stop event should carry cwd too: %+v", stop)
	}
	cur, _ := Cursor("cursor", []byte(`{"hook_event_name":"beforeSubmitPrompt","cwd":"/work/secret-project","prompt":"fix it"}`), now)
	if len(cur) != 1 || cur[0].Cwd != cwd {
		t.Errorf("cursor event cwd: %+v", cur)
	}
}
