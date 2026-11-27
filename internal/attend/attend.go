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
	// repeats): the first one keeps the full allowance, repeats earn only this much.
	LowEffortCap time.Duration
	// ThinkCap is how far back before a substantive prompt (one that clearly refers to the
	// project's own code) composing time is credited. It needs OS-presence evidence for at
	// least half of those minutes and never overlaps time already credited.
	ThinkCap time.Duration
	// LoopCap replaces WatchCap for the prompt that sits between a failing and a passing run
	// of the same test command (a real fail→pass loop, closed within LoopWindow).
	LoopCap time.Duration
	// RawCap bounds how much beyond the credited window is reported as unverified.
	RawCap time.Duration
}

// Default borrows Tally's substance caps: try-again prompts earn a minute, substantive prompts
// earn composing time, and a closed fail→pass loop earns up to 20 minutes of watching.
var Default = Config{Timeout: 2 * time.Minute, WatchCap: 5 * time.Minute, LowEffortCap: time.Minute,
	ThinkCap: 5 * time.Minute, LoopCap: 20 * time.Minute, RawCap: time.Hour}

// LoopWindow is how long a failing test run stays open for a later pass to close the loop.
const LoopWindow = time.Hour

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
	modeSpans(events, cfg, func(p string, from, to time.Time, _ Mode) { emit(p, from, to) }, nil)
}

// Unverified returns, per project, attention that looked real but exceeded a cap: the part of
// a prompt's watch window beyond its limit while the agent was still working and you stayed.
// It is reported next to attended time and never counts toward it.
func Unverified(events []event.Event, cfg Config) map[string]time.Duration {
	out := map[string]time.Duration{}
	modeSpans(events, cfg, func(string, time.Time, time.Time, Mode) {}, func(p string, d time.Duration) { out[p] += d })
	return out
}

// loopPrompts marks prompts (by index in the time-sorted tl) that close a fail→pass loop: the
// latest prompt of a project before a passing test run, when an earlier run of the same
// command failed within LoopWindow and before that prompt.
func loopPrompts(tl, tests []event.Event) map[int]bool {
	out := map[int]bool{}
	for i, pass := range tests {
		if pass.Pass == nil || !*pass.Pass || pass.CmdHash == "" {
			continue
		}
		// the loop must not have been closed already by an earlier pass of the same command
		var fail time.Time
		for j := i - 1; j >= 0; j-- {
			t := tests[j]
			if t.Project != pass.Project || t.CmdHash != pass.CmdHash || pass.Time.Sub(t.Time) > LoopWindow {
				continue
			}
			if t.Pass != nil && *t.Pass {
				break
			}
			fail = t.Time
		}
		if fail.IsZero() {
			continue
		}
		for k := len(tl) - 1; k >= 0; k-- {
			c := tl[k]
			if c.Kind == event.Prompt && c.Project == pass.Project && c.Time.After(fail) && c.Time.Before(pass.Time) {
				out[k] = true
				break
			}
		}
	}
	return out
}

// modeSpans is spans with the kind of attention each interval represents. emitU, if set,
// receives capped-out time per project.
func modeSpans(events []event.Event, cfg Config, emitM func(project string, from, to time.Time, m Mode), emitU func(project string, d time.Duration)) {
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
	var tl, stops, actives, tests []event.Event
	for _, e := range events {
		switch {
		case e.IsHumanActivity() || e.Kind == event.Idle:
			tl = append(tl, e)
		case e.Kind == event.Stop:
			stops = append(stops, e)
		case e.Kind == event.Active:
			actives = append(actives, e) // OS input: proof the human stayed at the keyboard
		case e.Kind == event.TestRun:
			tests = append(tests, e)
		}
	}
	byTime := func(s []event.Event) { sort.SliceStable(s, func(i, j int) bool { return s[i].Time.Before(s[j].Time) }) }
	byTime(tl)
	byTime(stops)
	byTime(actives)
	byTime(tests)
	loops := loopPrompts(tl, tests)
	var credited time.Time // end of the latest interval emitted, so think credit never double counts
	emitC := emit
	emit = func(p string, from, to time.Time, k event.Kind) {
		if to.After(credited) {
			credited = to
		}
		emitC(p, from, to, k)
	}

	// firstStopAfter: the first time the agent said it finished, for project, in (after, before].
	firstStopAfter := func(after, before time.Time, project string) time.Time {
		j := sort.Search(len(stops), func(k int) bool { return stops[k].Time.After(after) })
		for ; j < len(stops) && !stops[j].Time.After(before); j++ {
			if stops[j].Project == project {
				return stops[j].Time
			}
		}
		return time.Time{}
	}
	activeCount := func(after, before time.Time) int {
		j := sort.Search(len(actives), func(k int) bool { return actives[k].Time.After(after) })
		n := 0
		for ; j < len(actives) && !actives[j].Time.After(before); j++ {
			n++
		}
		return n
	}
	hasActive := func(after, before time.Time) bool {
		j := sort.Search(len(actives), func(k int) bool { return actives[k].Time.After(after) })
		return j < len(actives) && !actives[j].Time.After(before)
	}

	var lowTimes []time.Time
	for i := 0; i < len(tl); i++ {
		cur := tl[i]
		if cur.Kind == event.Idle {
			continue
		}
		var next time.Time
		if i+1 < len(tl) {
			next = tl[i+1].Time
		}
		if cur.Kind != event.Prompt {
			// hands-on (editor activity, hand edits): credit the gap to the next human event,
			// bounded by Timeout.
			if !next.IsZero() {
				if gap := next.Sub(cur.Time); gap > 0 && gap <= cfg.Timeout {
					emit(cur.Project, cur.Time, next, cur.Kind)
				}
			}
			continue
		}
		// Prompt watch credit: it ends when the agent says it finished (Stop), when you
		// re-engage (next human event), or at the cap — whichever is first. It counts only if
		// there is evidence you actually stayed: a following human event within the window, the
		// agent finishing while you waited, or recorded OS presence. Firing prompts at an idle or
		// unattended agent earns nothing.
		limit := cfg.WatchCap
		if loops[i] && cfg.LoopCap > limit {
			limit = cfg.LoopCap
		}
		// Think credit: composing a substantive prompt. Starts after everything already
		// credited and after the previous human event, and needs presence for half its minutes.
		if cur.Substantive() && cfg.ThinkCap > 0 {
			from := cur.Time.Add(-cfg.ThinkCap)
			if credited.After(from) {
				from = credited
			}
			if i > 0 && tl[i-1].Time.After(from) {
				from = tl[i-1].Time
			}
			if mins := int(cur.Time.Sub(from) / time.Minute); mins >= 1 && activeCount(from, cur.Time)*2 >= mins {
				emit(cur.Project, from, cur.Time, event.Heartbeat) // hands-on: nobody was prompting yet
			}
		}
		if cur.LowEffort() {
			for len(lowTimes) > 0 && cur.Time.Sub(lowTimes[0]) > LowEffortWindow {
				lowTimes = lowTimes[1:]
			}
			if len(lowTimes) > 0 && cfg.LowEffortCap > 0 {
				limit = cfg.LowEffortCap
			}
			lowTimes = append(lowTimes, cur.Time)
		}
		window := cur.Time.Add(limit)
		within := func(t time.Time) bool { return !t.IsZero() && t.After(cur.Time) && !t.After(window) }
		stop := firstStopAfter(cur.Time, window, cur.Project)
		if !(within(next) || within(stop) || hasActive(cur.Time, window)) {
			continue // prompted and left
		}
		end := window
		if within(next) && next.Before(end) {
			end = next
		}
		if within(stop) && stop.Before(end) {
			end = stop
		}
		if end.After(cur.Time) {
			emit(cur.Project, cur.Time, end, cur.Kind)
		}
		// Excess: the agent was still working and you stayed, but the cap ended the credit.
		if emitU != nil && !within(next) && !within(stop) {
			raw := cur.Time.Add(cfg.RawCap)
			if !next.IsZero() && next.Before(raw) {
				raw = next
			}
			if s := firstStopAfter(window, raw, cur.Project); !s.IsZero() && s.Before(raw) {
				raw = s
			}
			if raw.After(end) {
				emitU(cur.Project, raw.Sub(end))
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
	}, nil)
	return out
}
