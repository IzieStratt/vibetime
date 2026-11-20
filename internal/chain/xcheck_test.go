package chain

import (
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/event"
)

// TestChainMatchesJS pins the exact chain hash of a redacted prompt event so the JS
// reimplementation in e2e/console.test.mjs (which the browser test relies on to forge a
// valid bundle) stays byte-compatible. If the event schema or its JSON changes, update both
// this hash and the JS `red` object together.
func TestChainMatchesJS(t *testing.T) {
	e := event.Event{Time: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC), Kind: event.Prompt, Project: "cs2",
		PromptText: "please make the bomb timer beep faster near the end 0"}
	h, _ := Next(Genesis, e)
	const js = "4741340c2872cbb40c0fc3de01b815319a92eae98ccf7044324fb85211083eec"
	if h != js {
		t.Fatalf("Go chain hash %s != JS hash %s — update e2e/console.test.mjs `red` to match the event schema", h, js)
	}
}
