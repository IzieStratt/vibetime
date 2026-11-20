// Package event defines the local event schema shared by all harness plugins.
package event

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
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
	Active       Kind = "active"    // presence watcher: OS saw real input this minute (internal only)
	Stop         Kind = "stop"      // agent finished its turn
)

var validKinds = map[Kind]bool{SessionStart: true, Prompt: true, ToolCall: true, Edit: true, Heartbeat: true, Idle: true, Active: true, Stop: true}

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
	// PromptSHA is sha256(PromptText). The chain commits to this hash, not the text,
	// so a prompt can be withheld from a submission without breaking verification.
	PromptSHA string `json:"prompt_sha256,omitempty"`
	// PromptLen is the prompt length in characters (kept when text is redacted).
	PromptLen int `json:"prompt_len,omitempty"`
	// PromptNorm is sha256 of the normalized prompt (lowercased, whitespace-collapsed), committed
	// to the chain. Cross-account duplicate detection uses it, so reformatting a copied prompt
	// (whitespace/case) no longer dodges the check.
	PromptNorm string `json:"prompt_norm,omitempty"`
	// Repo is the normalized git remote (e.g. github.com/owner/name); binds time to a repo.
	Repo string `json:"repo,omitempty"`
}

// Validate checks required fields.
func (e Event) Validate() error {
	if e.Time.IsZero() {
		return errors.New("time is required")
	}
	if !validKinds[e.Kind] {
		return fmt.Errorf("unknown kind %q", e.Kind)
	}
	if e.Kind != Idle && e.Kind != Active && e.Project == "" {
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

// Redacted returns e with prompt text replaced by its hashes.
func (e Event) Redacted() Event {
	if e.PromptText != "" {
		sum := sha256.Sum256([]byte(e.PromptText))
		norm := sha256.Sum256([]byte(normalizePrompt(e.PromptText)))
		e.PromptLen = len([]rune(e.PromptText))
		e.PromptSHA = hex.EncodeToString(sum[:])
		e.PromptNorm = hex.EncodeToString(norm[:])
		e.PromptText = ""
	}
	return e
}

// normalizePrompt lowercases and collapses all runs of whitespace to a single space, so
// cosmetic reformatting of a copied prompt yields the same hash.
func normalizePrompt(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// DuplicateKey returns the hash used for cross-account duplicate detection, preferring the
// committed normalized hash and falling back to computing it from text when present.
func (e Event) DuplicateKey() string {
	if e.PromptNorm != "" {
		return e.PromptNorm
	}
	if e.PromptText != "" {
		sum := sha256.Sum256([]byte(normalizePrompt(e.PromptText)))
		return hex.EncodeToString(sum[:])
	}
	return e.PromptSHA
}

// PromptLength returns the prompt length whether or not the text was redacted.
func (e Event) PromptLength() int {
	if e.PromptText != "" {
		return len([]rune(e.PromptText))
	}
	return e.PromptLen
}

// LowEffortPromptLen: prompts shorter than this ("continue", "ok", "go") are low effort.
const LowEffortPromptLen = 20

// LowEffort reports whether e is a prompt known to be very short.
func (e Event) LowEffort() bool {
	if e.Kind != Prompt {
		return false
	}
	n := e.PromptLength()
	return n > 0 && n < LowEffortPromptLen
}

// Internal reports whether only the agent itself may record this kind. Plugins and other
// local processes cannot post presence: otherwise a script could fake being at the keyboard.
func (k Kind) Internal() bool { return k == Idle || k == Active }
