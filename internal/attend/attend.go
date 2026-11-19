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
	modeSpans(events, cfg, func(p string, from, to time.Time, _ Mode) { emit(p, from, to) })
}

// modeSpans is spans with the kind of attention each interval represents.
func modeSpans(events []event.Event, cfg Config, emitM func(project string, from, to time.Time, m Mode)) {
	// prompt spans: the first Timeout is steering (reading output, deciding), the rest is
	// watching the agent; spans started by editor activity or hand edits are hands-on.
	emit := func(p string, from, to time.Time, k event.Kind) {
		if k != event.Prompt {
			emitM(p, from, to, HandsOn)
			return
		}
		split := from.Add(cfg.Timeout)
		if !split.Before(to) {
			emitM(p, from, to, Steering)
			return
		}
		emitM(p, from, split, Steering)
		emitM(p, split, to, Watching)
	}
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

	var lowTimes []time.Time
	for i := 0; i < len(tl); i++ {
		cur := tl[i]
		if cur.Kind == event.Idle {
			continue
		}
		limit := cfg.Timeout
		if cur.Kind == event.Prompt {
			limit = cfg.WatchCap
			// A short prompt ("continue", "go") keeps the full allowance only if no other short
			// prompt happened in the previous LowEffortWindow, so alternating with a longer prompt
			// doesn't reset it.
			if cur.LowEffort() {
				for len(lowTimes) > 0 && cur.Time.Sub(lowTimes[0]) > LowEffortWindow {
					lowTimes = lowTimes[1:]
				}
				if len(lowTimes) > 0 && cfg.LowEffortCap > 0 {
					limit = cfg.LowEffortCap
				}
				lowTimes = append(lowTimes, cur.Time)
			}
		}
		if i+1 < len(tl) {
			if gap := tl[i+1].Time.Sub(cur.Time); gap > 0 && gap <= limit {
				emit(cur.Project, cur.Time, tl[i+1].Time, cur.Kind)
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
			// stops are sorted: jump to the first one after the prompt (no per-prompt full scan)
			j := sort.Search(len(stops), func(k int) bool { return stops[k].Time.After(cur.Time) })
			for ; j < len(stops) && !stops[j].Time.After(end); j++ {
				if stops[j].Project == cur.Project {
					emit(cur.Project, cur.Time, stops[j].Time, cur.Kind)
					break
				}
			}
		}
	}
}

// LowEffortWindow is how far back repeated short prompts are remembered.
const LowEffortWindow = 10 * time.Minute

// Total sums all projects.
func Total(m map[string]time.Duration) time.Duration {
	var t time.Duration
	for _, d := range m {
		t += d
	}
	return t
}

// Mode is what kind of attention an attended interval was.
type Mode int

const (
	HandsOn  Mode = iota // typing, editing by hand, working in the editor
	Steering             // prompting and reading/reviewing the result
	Watching             // watching the agent work after a prompt
)

// MinuteModes is attended time in one minute, split by mode.
type MinuteModes [3]time.Duration

// MinutesByMode returns per-project, per-minute attended time split by mode.
// The sum of the three modes equals Minutes for the same minute.
func MinutesByMode(events []event.Event, cfg Config) map[string]map[int64]*MinuteModes {
	out := map[string]map[int64]*MinuteModes{}
	modeSpans(events, cfg, func(p string, from, to time.Time, m Mode) {
		if out[p] == nil {
			out[p] = map[int64]*MinuteModes{}
		}
		for from.Before(to) {
			k := from.Truncate(time.Minute)
			end := k.Add(time.Minute)
			if end.After(to) {
				end = to
			}
			if out[p][k.Unix()] == nil {
				out[p][k.Unix()] = &MinuteModes{}
			}
			out[p][k.Unix()][m] += end.Sub(from)
			from = end
		}
	})
	return out
}
