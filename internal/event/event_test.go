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
