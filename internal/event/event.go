// Package event defines the local event schema shared by all harness plugins.
package event

import (
	"errors"
	"fmt"
	"time"
)

// Kind is the type of an event.
type Kind string

const (
	SessionStart Kind = "session_start"
	Prompt       Kind = "prompt"    // human submitted a prompt to an AI harness
	ToolCall     Kind = "tool_call" // the agent ran a tool (not human activity)
	Edit         Kind = "edit"      // a file edit; Human=true when typed by the user
	Heartbeat    Kind = "heartbeat" // editor activity (WakaTime-style)
	Idle         Kind = "idle"      // OS reports the user went idle / away
	Stop         Kind = "stop"      // agent finished its turn
)

var validKinds = map[Kind]bool{SessionStart: true, Prompt: true, ToolCall: true, Edit: true, Heartbeat: true, Idle: true, Stop: true}

// Event is one locally recorded signal. Content fields (PromptText) never leave the machine
// except inside an explicit submission bundle.
type Event struct {
	Time         time.Time `json:"time"`
	Kind         Kind      `json:"kind"`
	Project      string    `json:"project,omitempty"`
	Harness      string    `json:"harness,omitempty"`
	Model        string    `json:"model,omitempty"`
	Human        bool      `json:"human,omitempty"`
	Language     string    `json:"language,omitempty"`
	LinesAdded   int       `json:"lines_added,omitempty"`
	LinesRemoved int       `json:"lines_removed,omitempty"`
	PromptText   string    `json:"prompt_text,omitempty"`
}

// Validate checks required fields.
func (e Event) Validate() error {
	if e.Time.IsZero() {
		return errors.New("time is required")
	}
	if !validKinds[e.Kind] {
		return fmt.Errorf("unknown kind %q", e.Kind)
	}
	if e.Kind != Idle && e.Project == "" {
		return errors.New("project is required")
	}
	if e.LinesAdded < 0 || e.LinesRemoved < 0 {
		return errors.New("line counts must be non-negative")
	}
	return nil
}

// IsHumanActivity reports whether the event proves a human was attending.
func (e Event) IsHumanActivity() bool {
	switch e.Kind {
	case Prompt, Heartbeat, SessionStart:
		return true
	case Edit:
		return e.Human
	}
	return false
}
