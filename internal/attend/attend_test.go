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

func TestMinutesSplitAcrossBoundaries(t *testing.T) {
	// 08:00:30 -> 08:02:00 (heartbeats every 30s) on "a"
	evs := every(0.5, 2, 0.5, event.Heartbeat, "a")
	m := Minutes(evs, Default)["a"]
	if m[t0.Unix()] != 30*time.Second || m[t0.Add(time.Minute).Unix()] != time.Minute || len(m) != 2 {
		t.Fatalf("%v", m)
	}
	var sum time.Duration
	for _, d := range m {
		sum += d
	}
	if sum != Compute(evs, Default)["a"] {
		t.Fatal("Minutes and Compute disagree")
	}
}

func TestMinutesNeverOver60s(t *testing.T) {
	evs := append(every(0, 30, 0.25, event.Heartbeat, "a"), every(0.1, 30.1, 0.25, event.Heartbeat, "b")...)
	per := map[int64]time.Duration{}
	for _, mm := range Minutes(evs, Default) {
		for k, d := range mm {
			per[k] += d
		}
	}
	for k, d := range per {
		if d > time.Minute {
			t.Fatalf("minute %d has %v", k, d)
		}
	}
}

func prompt(min float64, text string) event.Event {
	e := at(min, event.Prompt, "a")
	e.PromptText = text
	return e
}

// B1: "continue" every 4 minutes while away.
func TestContinueFarmingDecays(t *testing.T) {
	var evs []event.Event
	for i := 0; i < 15; i++ {
		evs = append(evs, prompt(float64(i*4), "continue"))
	}
	got := Compute(evs, Default)["a"]
	// first prompt earns its 4 min gap, the 13 repeats earn nothing (gap 4m > 1m cap)
	if got != 4*time.Minute {
		t.Fatalf("got %v", got)
	}
	// real prompts at the same cadence earn full credit
	var real []event.Event
	for i := 0; i < 15; i++ {
		real = append(real, prompt(float64(i*4), "the recoil pattern is wrong after the 3rd shot, fix the spray table"))
	}
	if Compute(real, Default)["a"] != 56*time.Minute {
		t.Fatal("real prompts penalized")
	}
	// redacted low-effort prompts are still detected via prompt_len
	red := []event.Event{prompt(0, "go").Redacted(), prompt(0.5, "go").Redacted(), prompt(3, "go")}
	if Compute(red, Default)["a"] != 30*time.Second {
		t.Fatalf("redacted: %v", Compute(red, Default))
	}
}
