// Package attend computes attended (human-in-the-loop) time per project.
//
// Rules: human events across all projects are merged into one timeline, so the same
// minute is never counted twice. The gap after a human event is credited to that
// event's project when it is no longer than Timeout, or WatchCap after a prompt
// (the user watching the agent work). Agent-only events (tool calls, AI edits, stop)
// neither earn nor extend time. An Idle event ends the current gap immediately.
package attend

import (
	"sort"
	"time"

	"github.com/IzieStratt/vibetime/internal/event"
)

type Config struct {
	Timeout  time.Duration
	WatchCap time.Duration
}

var Default = Config{Timeout: 2 * time.Minute, WatchCap: 5 * time.Minute}

// Compute returns attended duration per project.
func Compute(events []event.Event, cfg Config) map[string]time.Duration {
	var tl []event.Event
	for _, e := range events {
		if e.IsHumanActivity() || e.Kind == event.Idle {
			tl = append(tl, e)
		}
	}
	sort.SliceStable(tl, func(i, j int) bool { return tl[i].Time.Before(tl[j].Time) })

	out := map[string]time.Duration{}
	for i := 0; i+1 < len(tl); i++ {
		cur, next := tl[i], tl[i+1]
		if cur.Kind == event.Idle {
			continue
		}
		limit := cfg.Timeout
		if cur.Kind == event.Prompt {
			limit = cfg.WatchCap
		}
		if gap := next.Time.Sub(cur.Time); gap > 0 && gap <= limit {
			out[cur.Project] += gap
		}
	}
	return out
}

// Total sums all projects.
func Total(m map[string]time.Duration) time.Duration {
	var t time.Duration
	for _, d := range m {
		t += d
	}
	return t
}
