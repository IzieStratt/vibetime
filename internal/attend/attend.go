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
	// LowEffortCap replaces WatchCap after consecutive low-effort prompts ("continue"
	// farming): the first one keeps the full allowance, repeats earn only this much.
	LowEffortCap time.Duration
}

var Default = Config{Timeout: 2 * time.Minute, WatchCap: 5 * time.Minute, LowEffortCap: time.Minute}

// Compute returns attended duration per project.
func Compute(events []event.Event, cfg Config) map[string]time.Duration {
	out := map[string]time.Duration{}
	spans(events, cfg, func(p string, from, to time.Time) { out[p] += to.Sub(from) })
	return out
}

// Minutes returns attended time per project per UTC minute (keyed by minute start, unix seconds).
func Minutes(events []event.Event, cfg Config) map[string]map[int64]time.Duration {
	out := map[string]map[int64]time.Duration{}
	spans(events, cfg, func(p string, from, to time.Time) {
		if out[p] == nil {
			out[p] = map[int64]time.Duration{}
		}
		for from.Before(to) {
			m := from.Truncate(time.Minute)
			end := m.Add(time.Minute)
			if end.After(to) {
				end = to
			}
			out[p][m.Unix()] += end.Sub(from)
			from = end
		}
	})
	return out
}

// spans calls emit for every credited interval.
func spans(events []event.Event, cfg Config, emit func(project string, from, to time.Time)) {
	var tl, stops []event.Event
	for _, e := range events {
		if e.IsHumanActivity() || e.Kind == event.Idle {
			tl = append(tl, e)
		} else if e.Kind == event.Stop {
			stops = append(stops, e)
		}
	}
	sort.SliceStable(stops, func(i, j int) bool { return stops[i].Time.Before(stops[j].Time) })
	sort.SliceStable(tl, func(i, j int) bool { return tl[i].Time.Before(tl[j].Time) })

	prevLow := false
	for i := 0; i < len(tl); i++ {
		cur := tl[i]
		if cur.Kind == event.Idle {
			continue
		}
		limit := cfg.Timeout
		if cur.Kind == event.Prompt {
			limit = cfg.WatchCap
			low := cur.LowEffort()
			if low && prevLow && cfg.LowEffortCap > 0 {
				limit = cfg.LowEffortCap
			}
			prevLow = low
		}
		if i+1 < len(tl) {
			if gap := tl[i+1].Time.Sub(cur.Time); gap > 0 && gap <= limit {
				emit(cur.Project, cur.Time, tl[i+1].Time)
				continue
			}
		}
		// The last prompt before a break: credit watching the agent until it stopped
		// (within the allowance and before any idle or later activity).
		if cur.Kind == event.Prompt {
			end := cur.Time.Add(limit)
			if i+1 < len(tl) && tl[i+1].Time.Before(end) {
				end = tl[i+1].Time
			}
			for _, s := range stops {
				if s.Project == cur.Project && s.Time.After(cur.Time) && !s.Time.After(end) {
					emit(cur.Project, cur.Time, s.Time)
					break
				}
			}
		}
	}
}

// Total sums all projects.
func Total(m map[string]time.Duration) time.Duration {
	var t time.Duration
	for _, d := range m {
		t += d
	}
	return t
}
