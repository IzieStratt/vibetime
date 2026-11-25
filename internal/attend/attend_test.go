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
func TestContinueRepeatsDecays(t *testing.T) {
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

// Found in a real Claude Code run: prompt, agent works 23s, stops, session ends.
func TestPromptUntilStopCounts(t *testing.T) {
	evs := []event.Event{at(0, event.SessionStart, "a"), at(0.01, event.Prompt, "a"), at(0.3, event.Edit, "a"), at(0.4, event.Stop, "a")}
	if got := Compute(evs, Default)["a"]; got != 24*time.Second {
		t.Fatalf("got %v", got)
	}
	// stop beyond the watch cap: nothing
	if got := Compute([]event.Event{at(0, event.Prompt, "a"), at(20, event.Stop, "a")}, Default)["a"]; got != 0 {
		t.Fatal(got)
	}
	// idle before the stop cuts it
	if got := Compute([]event.Event{at(0, event.Prompt, "a"), at(1, event.Idle, ""), at(3, event.Stop, "a")}, Default)["a"]; got != time.Minute {
		t.Fatal(got)
	}
	// other project's stop doesn't count
	if got := Compute([]event.Event{at(0, event.Prompt, "a"), at(1, event.Stop, "b")}, Default)["a"]; got != 0 {
		t.Fatal(got)
	}
}

func TestModes(t *testing.T) {
	// hands-on 2 min, then prompt with a 4 min gap: 2 min steering + 2 min watching
	evs := []event.Event{at(0, event.Heartbeat, "a"), at(1, event.Heartbeat, "a"), at(2, event.Prompt, "a"), at(6, event.Heartbeat, "a")}
	var tot MinuteModes
	for _, mm := range MinutesByMode(evs, Default)["a"] {
		for i := range mm {
			tot[i] += mm[i]
		}
	}
	if tot[HandsOn] != 2*time.Minute || tot[Steering] != 2*time.Minute || tot[Watching] != 2*time.Minute {
		t.Fatalf("%v", tot)
	}
	// every scenario: modes sum to Minutes exactly
	for _, evs := range [][]event.Event{evs, every(0, 60, 0.7, event.Heartbeat, "a"),
		{at(0, event.Prompt, "a"), at(0.5, event.Stop, "a")}, {prompt(0, "continue"), prompt(0.5, "continue"), prompt(3, "x")}} {
		plain := Minutes(evs, Default)
		for p, mins := range MinutesByMode(evs, Default) {
			for k, mm := range mins {
				if mm[0]+mm[1]+mm[2] != plain[p][k] {
					t.Fatalf("minute %d: modes %v != %v", k, mm, plain[p][k])
				}
			}
		}
	}
}

// Review finding #3: alternating "continue" with a longer prompt must not reset the cap.
func TestAlternatingRepeatsCapped(t *testing.T) {
	var evs []event.Event
	for i := 0; i < 30; i++ {
		text := "continue"
		if i%2 == 1 {
			text = "keep going with the next feature now"
		}
		evs = append(evs, prompt(float64(i*4), text))
	}
	got := Compute(evs, Default)["a"]
	// long prompts earn their 4 min (15 of them); repeat "continue"s inside 10 min earn nothing
	// (4 min gap > 1 min cap); only the first "continue" earns its 4 min.
	if got > 16*4*time.Minute {
		t.Fatalf("repeats earned %v", got)
	}
	// short prompts 15+ minutes apart are fine
	if Compute([]event.Event{prompt(0, "go"), prompt(3, "x"), prompt(20, "go"), prompt(23, "y")}, Default)["a"] != 6*time.Minute {
		t.Fatal("spaced short prompts penalized")
	}
}

// Review #3: watch credit after a prompt ends when the agent stops, so firing a prompt
// then re-prompting 4 min later (agent did 10s of work) earns ~10s, not the full 4 min.
func TestWatchClipsAtStop(t *testing.T) {
	m := time.Minute
	evs := []event.Event{
		{Time: t0, Kind: event.Prompt, Project: "a"},
		{Time: t0.Add(10 * time.Second), Kind: event.Stop, Project: "a"}, // agent finishes fast
		{Time: t0.Add(4 * m), Kind: event.Prompt, Project: "a"},          // human re-prompts 4 min later
		{Time: t0.Add(4*m + 10*time.Second), Kind: event.Stop, Project: "a"},
	}
	got := Compute(evs, Default)["a"]
	if got > 30*time.Second {
		t.Fatalf("prompt-and-wait credited %v, want <=30s (agent stopped at 10s)", got)
	}
	// but a prompt you actually stayed for (next human event soon) still credits the gap
	stayed := []event.Event{{Time: t0, Kind: event.Prompt, Project: "a"}, {Time: t0.Add(3 * m), Kind: event.Heartbeat, Project: "a"}}
	if Compute(stayed, Default)["a"] != 3*m {
		t.Fatalf("staying (next heartbeat 3m) should credit 3m, got %v", Compute(stayed, Default)["a"])
	}
	// presence (Active) alone proves you stayed watching even with no stop/next
	present := []event.Event{{Time: t0, Kind: event.Prompt, Project: "a"}, {Time: t0.Add(90 * time.Second), Kind: event.Active}}
	if Compute(present, Default)["a"] != 5*m {
		t.Fatalf("presence through the window should credit the cap, got %v", Compute(present, Default)["a"])
	}
	// prompt at an unattended agent (no stop, no next human, no presence) earns nothing
	un := []event.Event{{Time: t0, Kind: event.Prompt, Project: "a"}}
	for i := 1; i <= 10; i++ {
		un = append(un, event.Event{Time: t0.Add(time.Duration(i) * m), Kind: event.ToolCall, Project: "a"})
	}
	if Compute(un, Default)["a"] != 0 {
		t.Fatalf("unattended agent should earn 0, got %v", Compute(un, Default)["a"])
	}
}

func rel(e event.Event, pm int) event.Event { e.RelevancePM = &pm; e.PromptLen = 80; return e }

func testRun(min float64, cmd string, pass bool) event.Event {
	e := at(min, event.TestRun, "a")
	e.CmdHash, e.Pass = cmd, &pass
	return e
}

func TestThinkCreditNeedsSubstanceAndPresence(t *testing.T) {
	m := time.Minute
	prompt := func(pm int) event.Event { return rel(at(10, event.Prompt, "a"), pm) }
	presence := every(5.5, 9.5, 1, event.Active, "")
	// substantive + present for the 5 minutes before: 5m think + 1m steering (re-engages at 11)
	got := Compute(append(append([]event.Event{prompt(800)}, presence...), at(11, event.Heartbeat, "a")), Default)["a"]
	if got != 6*m {
		t.Errorf("substantive: got %v want 6m", got)
	}
	// same shape but trivially related prompt: no think credit
	if got := Compute(append(append([]event.Event{prompt(100)}, presence...), at(11, event.Heartbeat, "a")), Default)["a"]; got != m {
		t.Errorf("irrelevant: got %v want 1m", got)
	}
	// substantive but nobody there: nothing
	if got := Compute([]event.Event{prompt(800), at(11, event.Heartbeat, "a")}, Default)["a"]; got != m {
		t.Errorf("no presence: got %v want 1m", got)
	}
}

func TestThinkCreditNeverDoubleCounts(t *testing.T) {
	m := time.Minute
	evs := append(every(0, 9, 1, event.Heartbeat, "a"), rel(at(10, event.Prompt, "a"), 900), at(10.5, event.Heartbeat, "a"))
	evs = append(evs, every(5.5, 9.5, 1, event.Active, "")...)
	if got := Compute(evs, Default)["a"]; got != 10*m+30*time.Second {
		t.Errorf("got %v want 10m30s", got)
	}
}

func TestFailPassLoopExtendsWatch(t *testing.T) {
	m := time.Minute
	base := func(loop bool) []event.Event {
		evs := []event.Event{at(0, event.Prompt, "a"), testRun(1, "go-test", false), at(8, event.Active, ""), at(15, event.Active, "")}
		evs = append(evs, rel(at(2, event.Prompt, "a"), 700))
		if loop {
			evs = append(evs, testRun(14, "go-test", true))
		} else {
			evs = append(evs, testRun(14, "other", true))
		}
		return evs
	}
	// prompt at 2 is the one between the fail (1) and the pass (14); presence at 8 and 15 proves
	// the user stayed. Without the loop the 5m window (2→7) holds no proof of staying, so the
	// prompt earns nothing; with it the window is 20m and reaches the re-engagement at 16.
	with := Compute(append(base(true), at(16, event.Heartbeat, "a")), Default)["a"]
	without := Compute(append(base(false), at(16, event.Heartbeat, "a")), Default)["a"]
	if with <= without {
		t.Errorf("loop credit %v should exceed plain %v", with, without)
	}
	if with-without != 14*m {
		t.Errorf("loop should add the 14m from the prompt at 2 to the heartbeat at 16, got %v", with-without)
	}
}

func TestFailPassLoopNeedsPromptBetween(t *testing.T) {
	tl := []event.Event{at(0, event.Prompt, "a")}
	tests := []event.Event{testRun(1, "x", false), testRun(5, "x", true)}
	if len(loopPrompts(tl, tests)) != 0 {
		t.Error("a prompt before the failure must not count")
	}
	tl = append(tl, at(2, event.Prompt, "a"))
	if !loopPrompts(tl, tests)[1] {
		t.Error("prompt between fail and pass should close the loop")
	}
	// a pass in between already closed it
	tests = []event.Event{testRun(1, "x", false), testRun(3, "x", true), testRun(5, "x", true)}
	if loopPrompts(tl, tests)[1] && len(loopPrompts(tl, tests)) != 1 {
		t.Error("loop must close only once")
	}
}

func TestUnverifiedCapturesCappedExcess(t *testing.T) {
	m := time.Minute
	// prompt, user present, agent finishes only at minute 12: 5m credited, 7m beyond the cap.
	stop := at(12, event.Stop, "a")
	evs := []event.Event{at(0, event.Prompt, "a"), at(3, event.Active, ""), stop}
	// the stop is outside the 5m window, so presence (minute 3) qualifies the prompt
	if got := Compute(evs, Default)["a"]; got != 5*m {
		t.Fatalf("credited %v want 5m", got)
	}
	if got := Unverified(evs, Default)["a"]; got != 7*m {
		t.Errorf("unverified %v want 7m", got)
	}
	// nothing at the keyboard: neither credited nor unverified
	if got := Unverified([]event.Event{at(0, event.Prompt, "a"), stop}, Default)["a"]; got != 0 {
		t.Errorf("unattended excess must not be reported, got %v", got)
	}
}
