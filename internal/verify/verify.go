// Package verify checks a submission bundle against the server's live commitments and
// raises fraud flags. The client is untrusted: only events anchored by a commitment the
// server received in real time are counted (see docs/THREAT_MODEL.md).
package verify

import (
	"fmt"
	"math"
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
	good, unseen := withPresence(good)
	if unseen > 0 {
		res.flag("activity_without_presence", 30, "%d human-activity events had no keyboard/mouse input within %v; not counted", unseen, PresenceWindow)
	}
	res.Minutes = minutesIn(good, q)
	for _, s := range res.Minutes {
		res.VerifiedSeconds += s
	}
	res.ClaimedSeconds = secondsIn(all, q)

	proj := filter(good, q.Project)
	behaviour(&res, proj, good, q)
	repoChecks(&res, proj, q)
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

// behaviour implements the A1/B1/B2/B3/C3 detectors on anchored events.
func behaviour(res *Result, proj, timeline []event.Event, q Request) {
	sort.SliceStable(proj, func(i, j int) bool { return proj[i].Time.Before(proj[j].Time) })
	var prompts []event.Event
	output, lowEffort, redacted, humanWork, fast := 0, 0, 0, 0, 0
	var lastStop time.Time
	for i, e := range proj {
		switch e.Kind {
		case event.Prompt:
			prompts = append(prompts, e)
			if e.LowEffort() {
				lowEffort++
			}
			if e.PromptText == "" && e.PromptSHA != "" {
				redacted++
			}
			// B3: typed faster than a human could: chars per second since the agent last did
			// anything (its stop, or its last tool call/edit for harnesses without stop hooks)
			if !lastStop.IsZero() && e.PromptLength() > 40 {
				if dt := e.Time.Sub(lastStop).Seconds(); dt > 0 && float64(e.PromptLength())/dt > 15 {
					fast++
				}
			}
			// A1: did anything happen after this prompt?
			for _, n := range proj[i+1:] {
				if n.Kind == event.Prompt {
					break
				}
				if n.Kind == event.Edit || n.Kind == event.ToolCall {
					output++
					break
				}
			}
		case event.Stop, event.ToolCall:
			lastStop = e.Time
		case event.Heartbeat:
			humanWork++
		case event.Edit:
			if e.Human {
				humanWork++
			} else {
				lastStop = e.Time
			}
		}
	}
	n := len(prompts)
	if n >= 5 && output*2 < n {
		res.flag("prompts_without_output", 30, "%d of %d prompts produced no edits or tool calls", n-output, n)
	}
	if n >= 6 && lowEffort*2 > n {
		res.flag("low_effort_prompts", 15, "%d of %d prompts are under %d characters", lowEffort, n, event.LowEffortPromptLen)
	}
	if n >= 4 && redacted*5 > n*4 {
		// Redaction is the privacy default, so on its own it costs nothing. But prompt length is
		// self-reported and unverifiable once text is hidden, so when prompts are the only evidence
		// (no editor or hand-edit activity) heavy redaction hides what earned the time: send it to
		// a human, who can ask the submitter to reveal the prompts.
		w := 0
		if humanWork == 0 {
			w = 15
		}
		res.flag("heavy_redaction", w, "%d of %d prompts redacted (reviewer may ask to see them)", redacted, n)
	}
	if fast >= 3 {
		res.flag("superhuman_typing", 30, "%d prompts typed faster than 15 chars/s", fast)
	}
	if cv, ok := cadenceCV(prompts); ok && cv < 0.2 {
		res.flag("robotic_cadence", 30, "prompt intervals too regular (CV %.2f)", cv)
	}
	if n >= 10 && humanWork == 0 {
		res.flag("no_human_activity", 5, "no editor activity or hand edits, only prompts")
	}

	// B2/B3: presence. A real person goes idle; scripts and jigglers don't.
	verifiedH := float64(secondsIn(proj, q)) / 3600
	idles := 0
	for _, e := range timeline {
		if e.Kind == event.Idle {
			idles++
		}
	}
	if verifiedH >= 3 && idles == 0 {
		res.flag("no_presence_signal", 15, "%.1fh with no idle periods: presence detection missing or defeated", verifiedH)
	}
	// B3: inhuman day length, over any sliding 24h window (calendar days can be straddled)
	if start, d := busiest24h(attend.Minutes(proj, attend.Default)[q.Project]); d > 14*time.Hour {
		res.flag("long_day", 25, "%.1fh attended in the 24h from %s", d.Hours(), start.UTC().Format(time.RFC3339))
	}
}

// cadenceCV is the coefficient of variation of gaps between prompts (needs >= 10).
func cadenceCV(prompts []event.Event) (float64, bool) {
	if len(prompts) < 10 {
		return 0, false
	}
	var gaps []float64
	for i := 1; i < len(prompts); i++ {
		gaps = append(gaps, prompts[i].Time.Sub(prompts[i-1].Time).Seconds())
	}
	var mean float64
	for _, g := range gaps {
		mean += g
	}
	mean /= float64(len(gaps))
	if mean <= 0 {
		return 0, true
	}
	var v float64
	for _, g := range gaps {
		v += (g - mean) * (g - mean)
	}
	return math.Sqrt(v/float64(len(gaps))) / mean, true
}

// repoChecks implements C1 (laundering) and C2 (copied repo / no real output).
func repoChecks(res *Result, proj []event.Event, q Request) {
	if q.Repo == "" {
		res.flag("no_repo", 10, "submission has no repository")
		return
	}
	other := 0
	claimedLines := 0
	for _, e := range proj {
		if e.Repo != "" && e.Repo != q.Repo {
			other++
		}
		if e.Kind == event.Edit {
			claimedLines += e.LinesAdded + e.LinesRemoved
		}
	}
	if other > 0 {
		res.flag("repo_mismatch", 50, "%d events were recorded in a different repository", other)
	}
	if q.GitErr != nil {
		res.flag("git_unavailable", 5, "could not fetch git evidence: %v", q.GitErr)
		return
	}
	if q.Commits == nil {
		return
	}
	pushed, gitLines := 0, 0
	for _, c := range q.Commits {
		if !c.PushedAt.Before(q.From) && !c.PushedAt.After(q.To.Add(24*time.Hour)) {
			pushed++
			gitLines += c.Additions + c.Deletions
		}
	}
	hours := float64(res.VerifiedSeconds) / 3600
	if pushed == 0 && hours >= 1 {
		res.flag("no_pushes", 40, "%.1fh claimed but nothing was pushed to %s in the window", hours, q.Repo)
		return
	}
	if claimedLines >= 50 && gitLines > 0 {
		ratio := float64(claimedLines) / float64(gitLines)
		if ratio > 10 || ratio < 0.02 {
			res.flag("lines_mismatch", 25, "tracked edits changed %d lines but pushes changed %d", claimedLines, gitLines)
		}
	}
}

// busiest24h returns the start and total of the 24h window with the most attended time.
func busiest24h(mins map[int64]time.Duration) (time.Time, time.Duration) {
	keys := make([]int64, 0, len(mins))
	for k := range mins {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var best, cur time.Duration
	var bestStart int64
	lo := 0
	for hi, k := range keys {
		cur += mins[k]
		for keys[lo] <= k-24*3600 {
			cur -= mins[keys[lo]]
			lo++
		}
		if cur > best {
			best, bestStart = cur, keys[lo]
		}
		_ = hi
	}
	return time.Unix(bestStart, 0), best
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
