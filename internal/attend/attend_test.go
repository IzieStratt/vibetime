package attend

import (
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/event"
)

var t0 = time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)

func at(min float64, k event.Kind, p string) event.Event {
	return event.Event{Time: t0.Add(time.Duration(min * float64(time.Minute))), Kind: k, Project: p}
}

func every(start, end, step float64, k event.Kind, p string) []event.Event {
	var out []event.Event
	for m := start; m <= end; m += step {
		out = append(out, at(m, k, p))
	}
	return out
}

func TestScenarios(t *testing.T) {
	m := time.Minute
	cases := []struct {
		name string
		evs  []event.Event
		want map[string]time.Duration
	}{
		{"empty", nil, map[string]time.Duration{}},
		{"single event", []event.Event{at(0, event.Heartbeat, "a")}, map[string]time.Duration{}},
		{"flow state 3h", every(0, 180, 1, event.Heartbeat, "a"), map[string]time.Duration{"a": 180 * m}},
		{"5 min before school", every(0, 5, 0.5, event.Heartbeat, "a"), map[string]time.Duration{"a": 5 * m}},
		{"gap over timeout not counted", []event.Event{at(0, event.Heartbeat, "a"), at(3, event.Heartbeat, "a")}, map[string]time.Duration{}},
		{"prompt then watch agent 4min", []event.Event{at(0, event.Prompt, "a"), at(4, event.Heartbeat, "a")}, map[string]time.Duration{"a": 4 * m}},
		{"prompt then away 20min", []event.Event{at(0, event.Prompt, "a"), at(20, event.Heartbeat, "a")}, map[string]time.Duration{}},
		{"unattended agent earns nothing", append([]event.Event{at(0, event.Prompt, "a")}, every(1, 60, 1, event.ToolCall, "a")...), map[string]time.Duration{}},
		{"ai edits do not extend", []event.Event{at(0, event.Heartbeat, "a"), at(1, event.Edit, "a"), at(1.5, event.Heartbeat, "a")}, map[string]time.Duration{"a": 90 * time.Second}},
		{"human edits count", []event.Event{{Time: t0, Kind: event.Edit, Project: "a", Human: true}, at(1, event.Heartbeat, "a")}, map[string]time.Duration{"a": m}},
		{"idle cuts watch", []event.Event{at(0, event.Prompt, "a"), at(1, event.Idle, ""), at(4, event.Heartbeat, "a")}, map[string]time.Duration{"a": m}},
		{"multitask 5 projects", func() []event.Event {
			var e []event.Event
			for i, p := range []string{"a", "b", "c", "d", "e"} {
				e = append(e, every(float64(i*10), float64(i*10+9), 1, event.Heartbeat, p)...)
			}
			return e
		}(), map[string]time.Duration{"a": 10 * m, "b": 10 * m, "c": 10 * m, "d": 10 * m, "e": 9 * m}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Compute(c.evs, Default)
			if len(got) != len(c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
			for p, d := range c.want {
				if got[p] != d {
					t.Errorf("%s: got %v want %v", p, got[p], d)
				}
			}
		})
	}
}

func TestInterleavedNeverExceedsWallClock(t *testing.T) {
	// two projects sending heartbeats simultaneously must not double count
	evs := append(every(0, 60, 1, event.Heartbeat, "a"), every(0.5, 60.5, 1, event.Heartbeat, "b")...)
	got := Total(Compute(evs, Default))
	if got > 61*time.Minute {
		t.Fatalf("double counted: %v", got)
	}
}

func TestOrderIndependent(t *testing.T) {
	evs := every(0, 10, 1, event.Heartbeat, "a")
	rev := make([]event.Event, len(evs))
	for i := range evs {
		rev[len(evs)-1-i] = evs[i]
	}
	if Total(Compute(evs, Default)) != Total(Compute(rev, Default)) {
		t.Fatal("result depends on input order")
	}
}
