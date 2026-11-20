package event

import (
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		e    Event
		ok   bool
	}{
		{"ok", Event{Time: now, Kind: Prompt, Project: "p"}, true},
		{"idle no project", Event{Time: now, Kind: Idle}, true},
		{"no time", Event{Kind: Prompt, Project: "p"}, false},
		{"bad kind", Event{Time: now, Kind: "x", Project: "p"}, false},
		{"no project", Event{Time: now, Kind: Edit}, false},
		{"negative lines", Event{Time: now, Kind: Edit, Project: "p", LinesAdded: -1}, false},
	}
	for _, c := range cases {
		if err := c.e.Validate(); (err == nil) != c.ok {
			t.Errorf("%s: err=%v", c.name, err)
		}
	}
}

func TestIsHumanActivity(t *testing.T) {
	want := map[Kind]bool{Prompt: true, Heartbeat: true, SessionStart: true, ToolCall: false, Stop: false, Idle: false, Edit: false}
	for k, w := range want {
		if (Event{Kind: k}).IsHumanActivity() != w {
			t.Errorf("%s", k)
		}
	}
	if !(Event{Kind: Edit, Human: true}).IsHumanActivity() {
		t.Error("human edit")
	}
}

func TestPromptHelpers(t *testing.T) {
	e := Event{Kind: Prompt, PromptText: "héllo"}
	if e.PromptLength() != 5 || !e.LowEffort() {
		t.Fatal("len/low")
	}
	r := e.Redacted()
	if r.PromptText != "" || r.PromptLen != 5 || r.PromptLength() != 5 || !r.LowEffort() || r.Redacted() != r {
		t.Fatalf("%+v", r)
	}
	if (Event{Kind: Prompt}).LowEffort() {
		t.Fatal("unknown length is not low effort")
	}
	if (Event{Kind: Edit, PromptText: "x"}).LowEffort() {
		t.Fatal("non-prompt")
	}
	long := Event{Kind: Prompt, PromptText: "this prompt is definitely long enough"}
	if long.LowEffort() {
		t.Fatal("long")
	}
}

func TestDuplicateKeyNormalizes(t *testing.T) {
	a := Event{Kind: Prompt, PromptText: "Fix the  recoil\n PATTERN"}
	b := Event{Kind: Prompt, PromptText: "fix the recoil pattern"} // reformatted copy
	if a.Redacted().DuplicateKey() != b.Redacted().DuplicateKey() {
		t.Fatal("whitespace/case reformatting dodged duplicate detection")
	}
	// genuinely different prompts differ
	c := Event{Kind: Prompt, PromptText: "add a scoreboard"}
	if a.Redacted().DuplicateKey() == c.Redacted().DuplicateKey() {
		t.Fatal("distinct prompts collided")
	}
	// DuplicateKey works before redaction (from text) and matches the redacted key
	if a.DuplicateKey() != a.Redacted().DuplicateKey() {
		t.Fatal("pre/post redaction key mismatch")
	}
	// committed in the chain-visible redacted form
	if a.Redacted().PromptNorm == "" {
		t.Fatal("PromptNorm not committed")
	}
}
