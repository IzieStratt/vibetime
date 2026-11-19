package verify

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/chain"
	"github.com/IzieStratt/vibetime/internal/event"
)

var t0 = time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)

const repo = "github.com/kid/cs2"

// sim plays an agent: events are chained and the head is committed to the "server".
type sim struct {
	noPresence bool // a script posting events with nobody at the keyboard
	lastActive time.Time
	live       bool // commit once a minute, as the real agent does
	nextCommit time.Time
	prev, head string
	links      []chain.Link
	commits    []Commitment
}

func newSim() *sim { return &sim{prev: chain.Genesis, head: chain.Genesis} }

func (s *sim) add(e event.Event) {
	if !s.noPresence && e.IsHumanActivity() && !e.Time.Truncate(time.Minute).Equal(s.lastActive) {
		s.lastActive = e.Time.Truncate(time.Minute)
		s.add(event.Event{Time: e.Time, Kind: event.Active})
	}
	if s.live {
		if s.nextCommit.IsZero() {
			s.nextCommit = e.Time.Add(time.Minute)
		}
		for !s.nextCommit.After(e.Time) {
			s.commit(s.nextCommit)
			s.nextCommit = s.nextCommit.Add(time.Minute)
		}
	}
	if e.Repo == "" && e.Kind != event.Idle {
		e.Repo = repo
	}
	h, _ := chain.Next(s.head, e)
	s.links = append(s.links, chain.Link{Event: e, Hash: h})
	s.head = h
}
func (s *sim) commit(at time.Time) { s.commits = append(s.commits, Commitment{s.head, at}) }

func min(m float64) time.Time { return t0.Add(time.Duration(m * float64(time.Minute))) }

// honest: realistic session with varied prompts, agent output, editor activity, idle breaks.
func honest(hours int) *sim { return honestSalt(hours, "") }

func honestSalt(hours int, salt string) *sim {
	s := newSim()
	s.live = true
	m := 0.0
	for i := 0; m < float64(hours*60); i++ {
		gap := 2.0 + float64((i*7)%5) // varied 2..6 min
		s.add(event.Event{Time: min(m), Kind: event.Prompt, Project: "cs2", PromptText: fmt.Sprintf("make the AWP scope sway less when crouching, attempt %d%s", i, salt)})
		s.add(event.Event{Time: min(m + 0.5), Kind: event.Edit, Project: "cs2", LinesAdded: 3, LinesRemoved: 1})
		s.add(event.Event{Time: min(m + 1), Kind: event.Stop, Project: "cs2"})
		if i%15 == 14 {
			s.add(event.Event{Time: min(m + 1.2), Kind: event.Idle})
		}
		for h := m + 1.5; h < m+gap; h++ { // reading output, testing in the editor
			s.add(event.Event{Time: min(h), Kind: event.Heartbeat, Project: "cs2"})
		}
		m += gap
	}
	s.commit(min(m))
	return s
}

func req(s *sim) Request {
	return Request{Project: "cs2", Repo: repo, From: t0, To: min(24 * 60), Prev: s.prev, Links: s.links, Commitments: s.commits,
		Commits: []Commit{{SHA: "a", PushedAt: min(60), Additions: 300, Deletions: 100}}}
}

func codes(r Result) string {
	var c []string
	for _, f := range r.Flags {
		c = append(c, f.Code)
	}
	return strings.Join(c, ",")
}

func has(r Result, code string) bool { return strings.Contains(","+codes(r)+",", ","+code+",") }

func TestHonestSessionClean(t *testing.T) {
	r := Verify(req(honest(4)))
	if !r.Valid || r.Risk != 0 || r.Unanchored != 0 {
		t.Fatalf("honest flagged: risk=%d %s unanchored=%d", r.Risk, codes(r), r.Unanchored)
	}
	if r.VerifiedSeconds < 3*3600 || r.VerifiedSeconds != r.ClaimedSeconds {
		t.Fatalf("verified %d claimed %d", r.VerifiedSeconds, r.ClaimedSeconds)
	}
}

// A1: script posts bare "prompt" events with no resulting work.
func TestA1ScriptedPrompts(t *testing.T) {
	s := newSim()
	for i := 0; i < 60; i++ {
		s.add(event.Event{Time: min(float64(i * 4)), Kind: event.Prompt, Project: "cs2", PromptText: "implement the next feature from the roadmap please now"})
		s.commit(min(float64(i*4) + 0.5))
	}
	r := Verify(req(s))
	if !has(r, "prompts_without_output") || !has(r, "robotic_cadence") {
		t.Fatalf("missed: %s", codes(r))
	}
}

// A2 + A4 + D1: patched agent writes a backdated history and commits it all at once.
func TestA2BackfillCaught(t *testing.T) {
	s := honest(3)
	s.commits = []Commitment{{s.head, min(3*60 + 600)}} // single commit 10h later
	r := Verify(req(s))
	if !has(r, "backfilled") || r.VerifiedSeconds > 300 {
		t.Fatalf("backfill verified %ds flags %s", r.VerifiedSeconds, codes(r))
	}
}

// A4: patched agent never commits (offline fabrication).
func TestA4NeverCommitted(t *testing.T) {
	s := honest(2)
	s.commits = nil
	r := Verify(req(s))
	if r.VerifiedSeconds != 0 || !has(r, "unanchored") {
		t.Fatalf("%d %s", r.VerifiedSeconds, codes(r))
	}
}

// A5: editing history after the fact breaks the chain.
func TestA5TamperedBundle(t *testing.T) {
	s := honest(1)
	s.links[3].Event.LinesAdded = 9999
	if r := Verify(req(s)); r.Valid || !has(r, "chain_broken") || r.VerifiedSeconds != 0 {
		t.Fatal(codes(r))
	}
}

// D1: presenting a different/partial timeline than what was committed live.
func TestChainGap(t *testing.T) {
	s := honest(2)
	other := honestSalt(1, "x") // commitments from an unrelated chain on the same device
	s.commits = append(s.commits, other.commits...)
	if r := Verify(req(s)); !has(r, "chain_gap") {
		t.Fatal(codes(r))
	}
}

// B1: "continue" farming.
func TestB1LowEffortFarming(t *testing.T) {
	s := newSim()
	for i := 0; i < 40; i++ {
		m := float64(i*4) + float64(i%3)
		s.add(event.Event{Time: min(m), Kind: event.Prompt, Project: "cs2", PromptText: "continue"})
		s.add(event.Event{Time: min(m + 0.3), Kind: event.ToolCall, Project: "cs2"})
		s.commit(min(m + 0.5))
	}
	r := Verify(req(s))
	if !has(r, "low_effort_prompts") || r.VerifiedSeconds > 10*60 {
		t.Fatalf("verified %ds %s", r.VerifiedSeconds, codes(r))
	}
}

// B2/B3: hours of activity with no idle at all (jiggler or bot).
func TestB2NoPresence(t *testing.T) {
	s := honest(5)
	var keep []event.Event
	for _, l := range s.links {
		if l.Event.Kind != event.Idle {
			keep = append(keep, l.Event)
		}
	}
	n := newSim()
	for i, e := range keep {
		n.add(e)
		n.commit(e.Time.Add(20 * time.Second))
		_ = i
	}
	if r := Verify(req(n)); !has(r, "no_presence_signal") {
		t.Fatal(codes(r))
	}
}

// B3: second LLM driving the harness: long prompts typed instantly, metronomic cadence, 16h day.
func TestB3AgentDrivingAgent(t *testing.T) {
	s := newSim()
	for i := 0; i < 16*60/2; i++ {
		m := float64(i * 2)
		s.add(event.Event{Time: min(m), Kind: event.Prompt, Project: "cs2",
			PromptText: "Now refactor the weapon system so that recoil, spread and sway share one config file with validation"})
		s.add(event.Event{Time: min(m + 0.5), Kind: event.Edit, Project: "cs2", LinesAdded: 2})
		s.add(event.Event{Time: min(m + 1.9), Kind: event.Stop, Project: "cs2"})
		s.commit(min(m + 1))
		s.commit(min(m + 1.95))
	}
	r := Verify(req(s))
	for _, c := range []string{"superhuman_typing", "robotic_cadence", "long_day", "no_presence_signal"} {
		if !has(r, c) {
			t.Errorf("missing %s in %s", c, codes(r))
		}
	}
}

// B4: two devices on one project → handled by the server's per-minute cap (see server tests);
// within one device the timeline merge prevents double counting.
func TestB4SameDeviceParallel(t *testing.T) {
	s := newSim()
	for i := 0; i < 60; i++ {
		s.add(event.Event{Time: min(float64(i)), Kind: event.Heartbeat, Project: "cs2"})
		s.add(event.Event{Time: min(float64(i) + 0.5), Kind: event.Heartbeat, Project: "other"})
		s.commit(min(float64(i) + 0.7))
	}
	if r := Verify(req(s)); r.VerifiedSeconds > 31*60 {
		t.Fatalf("double counted: %d", r.VerifiedSeconds)
	}
}

// C1: time recorded in one repo submitted for another.
func TestC1Laundering(t *testing.T) {
	// time genuinely recorded in another repo, submitted as cs2
	n := newSim()
	n.live = true
	for _, l := range honest(2).links {
		e := l.Event
		if e.Kind != event.Idle {
			e.Repo = "github.com/kid/other-thing"
		}
		n.add(e)
	}
	if r := Verify(req(n)); !has(r, "repo_mismatch") {
		t.Fatal(codes(r))
	}
}

// C2: hours claimed on a repo with nothing pushed, or wildly mismatched line counts.
func TestC2GitEvidence(t *testing.T) {
	q := req(honest(2))
	q.Commits = []Commit{}
	if r := Verify(q); !has(r, "no_pushes") {
		t.Fatal(codes(r))
	}
	q.Commits = []Commit{{PushedAt: min(30), Additions: 20000}}
	if r := Verify(q); !has(r, "lines_mismatch") {
		t.Fatal(codes(r))
	}
	q.Commits = []Commit{{PushedAt: min(-600), Additions: 100}} // pushed before window
	if r := Verify(q); !has(r, "no_pushes") {
		t.Fatal(codes(r))
	}
	q.Commits, q.GitErr = nil, errors.New("rate limited")
	if r := Verify(q); !has(r, "git_unavailable") {
		t.Fatal(codes(r))
	}
	q.GitErr, q.Repo = nil, ""
	if r := Verify(q); !has(r, "no_repo") {
		t.Fatal(codes(r))
	}
}

// C3: redact every prompt.
func TestC3HeavyRedaction(t *testing.T) {
	s := honest(1)
	for i := range s.links {
		s.links[i].Event = s.links[i].Event.Redacted()
	}
	r := Verify(req(s))
	if !r.Valid || !has(r, "heavy_redaction") {
		t.Fatalf("valid=%v %s", r.Valid, codes(r))
	}
}

func TestNoHumanActivity(t *testing.T) {
	s := newSim()
	for i := 0; i < 12; i++ {
		m := float64(i*3) + float64(i%4)
		s.add(event.Event{Time: min(m), Kind: event.Prompt, Project: "cs2", PromptText: "please carry on implementing the scoreboard and the buy menu"})
		s.add(event.Event{Time: min(m + 1), Kind: event.Edit, Project: "cs2", LinesAdded: 4})
		s.commit(min(m + 1.2))
	}
	if r := Verify(req(s)); !has(r, "no_human_activity") {
		t.Fatal(codes(r))
	}
}

func TestCadenceEdgeCases(t *testing.T) {
	if _, ok := cadenceCV(nil); ok {
		t.Fatal("too few")
	}
	same := make([]event.Event, 10)
	if cv, ok := cadenceCV(same); !ok || cv != 0 {
		t.Fatal("zero-gap prompts are maximally robotic")
	}
}

func TestBusiest24h(t *testing.T) {
	if _, d := busiest24h(nil); d != 0 {
		t.Fatal("empty")
	}
	m := map[int64]time.Duration{}
	base := t0.Unix()
	for i := int64(0); i < 20*60; i++ { // 20h straight, straddling midnight
		m[base+i*60] = time.Minute
	}
	m[base+40*3600] = time.Minute
	if _, d := busiest24h(m); d != 20*time.Hour {
		t.Fatal(d)
	}
}

// Found in a real run: the agent commits its (empty) head before the first event.
func TestCommitmentOfPrevIsNotAGap(t *testing.T) {
	s := honest(1)
	s.commits = append([]Commitment{{chain.Genesis, t0}}, s.commits...)
	if r := Verify(req(s)); has(r, "chain_gap") {
		t.Fatal(codes(r))
	}
}

// Review finding #1: a local script posts heartbeats (and could post idles) over loopback.
// With no OS input recorded by the agent's own watcher, none of it counts.
func TestForgedHeartbeatsWithoutPresence(t *testing.T) {
	s := newSim()
	s.live, s.noPresence = true, true
	for i := 0; i < 180; i++ {
		s.add(event.Event{Time: min(float64(i)), Kind: event.Heartbeat, Project: "cs2"})
	}
	r := Verify(req(s))
	if r.VerifiedSeconds != 0 || !has(r, "activity_without_presence") {
		t.Fatalf("verified %ds %s", r.VerifiedSeconds, codes(r))
	}
}

// Review finding #7: a harness with no stop hooks must still get the typing-speed check.
func TestSuperhumanTypingWithoutStops(t *testing.T) {
	s := newSim()
	s.live = true
	m := 0.0
	for i := 0; i < 20; i++ {
		s.add(event.Event{Time: min(m), Kind: event.Prompt, Project: "cs2",
			PromptText: "Refactor the netcode so client prediction and server reconciliation share one tick loop"})
		s.add(event.Event{Time: min(m + 0.5), Kind: event.ToolCall, Project: "cs2"})
		m += 0.9 + float64(i%4)*0.3 // agent edits, then the bot answers 3s later
		s.add(event.Event{Time: min(m - 0.05), Kind: event.Edit, Project: "cs2", LinesAdded: 3})
	}
	s.commit(min(m + 1))
	if r := Verify(req(s)); !has(r, "superhuman_typing") {
		t.Fatal(codes(r))
	}
}
