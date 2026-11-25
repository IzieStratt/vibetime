//go:build !private

package verify

import "github.com/IzieStratt/vibetime/internal/event"

// analyze is the hook for behavioural analysis. The public build ships none: the heuristics
// are deployed server-side from a private module. Without them nothing can be auto-approved,
// so every submission goes to a human reviewer.
func analyze(res *Result, _, _, _ []event.Event, _ Request) {
	res.flag("manual_review", 1, "behavioural analysis is not installed on this server; human review required")
}
