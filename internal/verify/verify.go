// Package verify checks a submission bundle against the server's live commitments. The
// client is untrusted: only events anchored by a commitment the server received in real time
// are counted. Behavioural analysis is a private, server-side add-on (see analyze_public.go).
package verify

import (
	"fmt"
	"sort"
	"time"

	"github.com/IzieStratt/vibetime/internal/attend"
	"github.com/IzieStratt/vibetime/internal/chain"
	"github.com/IzieStratt/vibetime/internal/event"
)

// Commitment is a chain head the server received from a device, stamped with server time.
type Commitment struct {
	Head       string
	ReceivedAt time.Time
}

// Commit is git evidence from a third party (e.g. GitHub push events).
type Commit struct {
	SHA       string
	PushedAt  time.Time
	Additions int
	Deletions int
}

// Request is everything needed to verify one submission.
type Request struct {
	Project     string
	Repo        string // normalized; "" skips repo checks
	From, To    time.Time
	Prev        string // chain hash before Links[0]
	Links       []chain.Link
	Commitments []Commitment // all commitments for the device, any order
	Commits     []Commit     // nil = git evidence unavailable
	GitErr      error
}

// Flag is a reviewer-facing finding.
type Flag struct {
	Code   string `json:"code"`
	Weight int    `json:"weight"` // contribution to risk; 0 = informational
	Detail string `json:"detail"`
}

type Result struct {
	Valid           bool   `json:"valid"` // chain intact
	VerifiedSeconds int64  `json:"verified_seconds"`
	ClaimedSeconds  int64  `json:"claimed_seconds"` // all events, anchored or not
	Anchored        int    `json:"anchored_events"`
	Unanchored      int    `json:"unanchored_events"`
	Flags           []Flag `json:"flags"`
	Risk            int    `json:"risk"`
	// Minutes is verified seconds per minute (unix) for the project; used to stop the same
	// time being credited by more than one submission or device.
	Minutes map[int64]int64 `json:"-"`
}

// AnchorSlack is how long after an event its covering commitment may arrive.
// The agent commits every minute, so anything older was backfilled.
const AnchorSlack = 3 * time.Minute

func (r *Result) flag(code string, weight int, format string, a ...any) {
	r.Flags = append(r.Flags, Flag{code, weight, fmt.Sprintf(format, a...)})
	r.Risk += weight
}

// Verify runs every check. It never trusts the bundle's own timestamps beyond what the
// commitments prove.
func Verify(q Request) Result {
	var res Result
	if i, err := chain.Verify(q.Prev, q.Links); i != -1 {
		res.flag("chain_broken", 100, "hash chain invalid at link %d: %v", i, err)
		return res
	}
	res.Valid = true

	// index chain positions
	pos := map[string]int{}
	preceding := map[string]bool{q.Prev: true, chain.Genesis: true} // heads committed before the bundle starts
	for i, l := range q.Links {
		pos[l.Hash] = i
	}
	cs := append([]Commitment(nil), q.Commitments...)
	sort.Slice(cs, func(i, j int) bool { return cs[i].ReceivedAt.Before(cs[j].ReceivedAt) })

	// D1: every commitment received inside the window must be part of this bundle,
	// otherwise the user is hiding part of the timeline (or presenting a different one).
	missing := 0
	for _, c := range cs {
		if !c.ReceivedAt.Before(q.From) && !c.ReceivedAt.After(q.To.Add(AnchorSlack)) {
			if _, ok := pos[c.Head]; !ok && !preceding[c.Head] {
				missing++
			}
		}
	}
	if missing > 0 {
		res.flag("chain_gap", 40, "%d commitments in the window are not in the bundle", missing)
	}

	// anchor each link: the earliest commitment at or after its chain position
	anchor := make([]time.Time, len(q.Links))
	for _, c := range cs {
		if p, ok := pos[c.Head]; ok {
			for i := p; i >= 0 && anchor[i].IsZero(); i-- {
				anchor[i] = c.ReceivedAt
			}
		}
	}
	var good, all []event.Event
	late := 0
	for i, l := range q.Links {
		e := l.Event
		all = append(all, e)
		if e.Project != q.Project {
			// other projects and idle events still shape the timeline (no double counting)
			if !anchor[i].IsZero() && within(e.Time, anchor[i]) {
				good = append(good, e)
			}
			continue
		}
		switch {
		case anchor[i].IsZero():
			res.Unanchored++
		case !within(e.Time, anchor[i]):
			res.Unanchored++
			late++
		default:
			res.Anchored++
			good = append(good, e)
		}
	}
	if late > 0 {
		res.flag("backfilled", 50, "%d events claim times more than %v before the server saw them", late, AnchorSlack)
	}
	if res.Unanchored-late > 0 {
		res.flag("unanchored", 20, "%d events were never committed to the server", res.Unanchored-late)
	}

	// B2 / forged local events: human activity only counts when the agent's own presence
	// watcher saw real keyboard or mouse input around it. Plugins cannot post presence.
	// Keep the pre-presence timeline: unbacked prompts are dropped below, and B1a needs to see them.
	beforePresence := append([]event.Event(nil), good...)
	good, unseen := withPresence(good)
	if unseen > 0 {
		res.flag("activity_without_presence", 30, "%d human-activity events had no keyboard/mouse input within %v; not counted", unseen, PresenceWindow)
	}
	res.Minutes = minutesIn(good, q)
	for _, s := range res.Minutes {
		res.VerifiedSeconds += s
	}
	res.ClaimedSeconds = secondsIn(all, q)

	analyze(&res, beforePresence, filter(good, q.Project), good, q)
	return res
}

func within(t, anchoredAt time.Time) bool {
	return !t.After(anchoredAt.Add(time.Minute)) && anchoredAt.Sub(t) <= AnchorSlack
}

func filter(evs []event.Event, project string) []event.Event {
	var out []event.Event
	for _, e := range evs {
		if e.Project == project {
			out = append(out, e)
		}
	}
	return out
}

func secondsIn(evs []event.Event, q Request) int64 {
	var total time.Duration
	for m, d := range attend.Minutes(evs, attend.Default)[q.Project] {
		t := time.Unix(m, 0)
		if !t.Before(q.From.Truncate(time.Minute)) && t.Before(q.To) {
			total += d
		}
	}
	return int64(total / time.Second)
}

// PresenceWindow: how close a human-activity event must be to recorded OS input.
const PresenceWindow = 90 * time.Second

// withPresence drops human-activity events (prompts, heartbeats, hand edits, session
// starts) that have no Active presence event within PresenceWindow. Agent events are kept:
// they never earn time on their own.
func withPresence(evs []event.Event) ([]event.Event, int) {
	var active []time.Time
	for _, e := range evs {
		if e.Kind == event.Active {
			active = append(active, e.Time)
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].Before(active[j]) })
	near := func(t time.Time) bool {
		i := sort.Search(len(active), func(i int) bool { return !active[i].Before(t.Add(-PresenceWindow)) })
		return i < len(active) && !active[i].After(t.Add(PresenceWindow))
	}
	out := evs[:0:0]
	dropped := 0
	for _, e := range evs {
		if e.IsHumanActivity() && !near(e.Time) {
			dropped++
			continue
		}
		out = append(out, e)
	}
	return out, dropped
}

func minutesIn(evs []event.Event, q Request) map[int64]int64 {
	out := map[int64]int64{}
	for m, d := range attend.Minutes(evs, attend.Default)[q.Project] {
		t := time.Unix(m, 0)
		if !t.Before(q.From.Truncate(time.Minute)) && t.Before(q.To) {
			out[m] = int64(d / time.Second)
		}
	}
	return out
}
