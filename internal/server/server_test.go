package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/chain"
	"github.com/IzieStratt/vibetime/internal/event"
	"github.com/IzieStratt/vibetime/internal/verify"
)

var t0 = time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)

type env struct {
	t   *testing.T
	s   *Server
	ts  *httptest.Server
	now time.Time
	mu  sync.Mutex
}

func newEnv(t *testing.T) *env {
	s, err := Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, s: s, now: t0}
	s.Now = func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now }
	s.Rand = func() float64 { return 0.99 }
	s.OpenSignup = true
	e.ts = httptest.NewServer(s.Handler())
	t.Cleanup(func() { e.ts.Close(); s.Close() })
	return e
}

func (e *env) at(t time.Time) { e.mu.Lock(); e.now = t; e.mu.Unlock() }

func (e *env) call(method, path, tok string, body any) (int, []byte) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, b
}

func (e *env) user(handle string) string {
	code, b := e.call("POST", "/api/v1/signup", "", map[string]string{"handle": handle})
	if code != 201 {
		e.t.Fatalf("signup %d %s", code, b)
	}
	var m map[string]string
	json.Unmarshal(b, &m)
	return m["token"]
}

const dev = "device-0001"

// agent simulates a real vibetime agent: events go into the chain and the head is synced
// every minute with the minute aggregates.
type agent struct {
	e     *env
	tok   string
	dev   string
	head  string
	links []chain.Link
}

func (e *env) agent(tok string) *agent { return &agent{e: e, tok: tok, dev: dev, head: chain.Genesis} }

func (a *agent) add(ev event.Event) {
	h, _ := chain.Next(a.head, ev)
	a.links = append(a.links, chain.Link{Event: ev, Hash: h})
	a.head = h
}

func (a *agent) sync(at time.Time, mins ...SyncMinute) map[string]int {
	a.e.at(at)
	code, b := a.e.call("POST", "/api/v1/sync", a.tok, SyncRequest{Device: a.dev, Head: a.head, Count: len(a.links), Minutes: mins})
	if code != 200 {
		a.e.t.Fatalf("sync %d %s", code, b)
	}
	var m map[string]int
	json.Unmarshal(b, &m)
	return m
}

// session runs an honest 90 minute session with live syncing.
func (a *agent) session(project, salt string) {
	m := t0
	gaps := []int{3, 5, 4, 3, 6} // humans are irregular (robotic_cadence catches fixed intervals)
	for i := 0; i < 25; i++ {
		if i > 0 {
			m = m.Add(time.Duration(gaps[i%5]) * time.Minute)
		}
		a.add(event.Event{Time: m, Kind: event.Prompt, Project: project, Repo: "github.com/kid/" + project,
			PromptText: fmt.Sprintf("tune the smoke grenade fade so it lasts %d seconds longer %s", i, salt)})
		a.add(event.Event{Time: m.Add(30 * time.Second), Kind: event.Edit, Project: project, Repo: "github.com/kid/" + project, LinesAdded: 2})
		a.add(event.Event{Time: m.Add(time.Minute), Kind: event.Heartbeat, Project: project, Repo: "github.com/kid/" + project})
		a.sync(m.Add(70 * time.Second))
		a.add(event.Event{Time: m.Add(2 * time.Minute), Kind: event.Heartbeat, Project: project, Repo: "github.com/kid/" + project})
		a.sync(m.Add(130 * time.Second))
		if i%10 == 9 {
			a.add(event.Event{Time: m.Add(150 * time.Second), Kind: event.Idle})
			a.sync(m.Add(170 * time.Second))
		}
	}
}

func (a *agent) submit(project string) (int, Submission) {
	code, b := a.e.call("POST", "/api/v1/submissions", a.tok, SubmitRequest{Device: a.dev, Project: project, Repo: "github.com/kid/" + project,
		From: t0, To: t0.Add(2 * time.Hour), Prev: chain.Genesis, Links: a.links})
	var s Submission
	json.Unmarshal(b, &s)
	if code != 201 {
		a.e.t.Logf("submit: %s", b)
	}
	return code, s
}

func TestSignupAndAuth(t *testing.T) {
	e := newEnv(t)
	e.user("rowan")
	if c, _ := e.call("POST", "/api/v1/signup", "", map[string]string{"handle": "rowan"}); c != 400 {
		t.Fatal("dup handle")
	}
	if c, _ := e.call("POST", "/api/v1/signup", "", map[string]string{"handle": "Bad Handle!"}); c != 400 {
		t.Fatal("bad handle")
	}
	if c, _ := e.call("POST", "/api/v1/signup", "", "nope"); c != 400 {
		t.Fatal("bad json")
	}
	e.s.OpenSignup = false
	if c, _ := e.call("POST", "/api/v1/signup", "", map[string]string{"handle": "x2"}); c != 403 {
		t.Fatal("closed signup")
	}
	if c, _ := e.call("POST", "/api/v1/sync", "", nil); c != 401 {
		t.Fatal("no token")
	}
	if c, _ := e.call("POST", "/api/v1/sync", "vt_wrong", nil); c != 401 {
		t.Fatal("bad token")
	}
	if c, _ := e.call("GET", "/healthz", "", nil); c != 200 {
		t.Fatal("health")
	}
}

// A2 at the server: only fresh minutes are accepted.
func TestSyncRejectsBackfillAndClamps(t *testing.T) {
	e := newEnv(t)
	tok := e.user("rowan")
	a := e.agent(tok)
	a.add(event.Event{Time: t0, Kind: event.Prompt, Project: "cs2"})
	now := t0.Add(30 * time.Minute)
	m := a.sync(now,
		SyncMinute{"cs2", now.Add(-2 * time.Minute).Unix(), 60},  // ok
		SyncMinute{"cs2", now.Add(-time.Minute).Unix(), 500},     // ok, clamped to 60
		SyncMinute{"cs2", now.Add(-20 * time.Minute).Unix(), 60}, // too old
		SyncMinute{"cs2", now.Add(time.Hour).Unix(), 60},         // future
		SyncMinute{"cs2", now.Unix() - 61, 60},                   // not minute aligned
		SyncMinute{"", now.Add(-3 * time.Minute).Unix(), 60},     // no project
	)
	if m["accepted"] != 2 || m["rejected"] != 4 {
		t.Fatal(m)
	}
	_, b := e.call("GET", "/api/v1/users/rowan/stats", "", nil)
	var st Stats
	json.Unmarshal(b, &st)
	if st.TodaySeconds != 120 || st.Projects["cs2"] != 120 {
		t.Fatalf("%s", b)
	}
	if c, _ := e.call("POST", "/api/v1/sync", tok, SyncRequest{Device: "x", Head: a.head}); c != 400 {
		t.Fatal("bad device accepted")
	}
	if c, _ := e.call("POST", "/api/v1/sync", tok, "garbage"); c != 400 {
		t.Fatal("bad json")
	}
}

// B4: two devices reporting the same minutes cannot exceed 60s per minute.
func TestCrossDeviceMerge(t *testing.T) {
	e := newEnv(t)
	tok := e.user("rowan")
	a, b := e.agent(tok), e.agent(tok)
	b.dev = "device-0002"
	a.add(event.Event{Time: t0, Kind: event.Prompt, Project: "cs2"})
	b.add(event.Event{Time: t0, Kind: event.Prompt, Project: "site"})
	now := t0.Add(10 * time.Minute)
	for i := 1; i <= 5; i++ {
		min := now.Add(-time.Duration(i) * time.Minute).Unix()
		a.sync(now, SyncMinute{"cs2", min, 60})
		b.sync(now, SyncMinute{"site", min, 60})
	}
	_, body := e.call("GET", "/api/v1/users/rowan/stats", "", nil)
	var st Stats
	json.Unmarshal(body, &st)
	if st.TodaySeconds != 300 || st.WeekSeconds != 300 || st.Projects["cs2"] != 150 || st.Projects["site"] != 150 {
		t.Fatalf("double counted across devices: %s", body)
	}
}

func TestPrivacyAndProfile(t *testing.T) {
	e := newEnv(t)
	tok := e.user("rowan")
	if c, b := e.call("GET", "/u/rowan", "", nil); c != 200 || !strings.Contains(string(b), "rowan") || !strings.Contains(string(b), "EventSource") {
		t.Fatal("profile")
	}
	if c, _ := e.call("POST", "/api/v1/me", tok, map[string]bool{"public": false}); c != 200 {
		t.Fatal("update me")
	}
	for _, p := range []string{"/u/rowan", "/api/v1/users/rowan/stats", "/api/v1/users/rowan/live", "/u/nobody"} {
		if c, _ := e.call("GET", p, "", nil); c != 404 {
			t.Errorf("%s visible while private", p)
		}
	}
	if c, _ := e.call("POST", "/api/v1/me", tok, "x"); c != 400 {
		t.Fatal("bad json")
	}
}

func TestLiveSSE(t *testing.T) {
	e := newEnv(t)
	tok := e.user("rowan")
	resp, err := http.Get(e.ts.URL + "/api/v1/users/rowan/live")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)
	next := func() Stats {
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if d, ok := strings.CutPrefix(line, "data: "); ok {
				var st Stats
				json.Unmarshal([]byte(d), &st)
				return st
			}
		}
	}
	if st := next(); st.TodaySeconds != 0 {
		t.Fatal("initial")
	}
	a := e.agent(tok)
	a.add(event.Event{Time: t0, Kind: event.Prompt, Project: "cs2"})
	now := t0.Add(5 * time.Minute)
	go a.sync(now, SyncMinute{"cs2", now.Add(-time.Minute).Unix(), 42})
	done := make(chan Stats)
	go func() { done <- next() }()
	select {
	case st := <-done:
		if st.TodaySeconds != 42 {
			t.Fatalf("live update %+v", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no live update")
	}
}

type fakeGit struct {
	commits []verify.Commit
	err     error
}

func (f fakeGit) Commits(string, time.Time, time.Time) ([]verify.Commit, error) {
	return f.commits, f.err
}

func TestHonestSubmissionAutoApprovedWithWebhook(t *testing.T) {
	e := newEnv(t)
	hooks := make(chan Submission, 4)
	wh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var s Submission
		json.NewDecoder(r.Body).Decode(&s)
		hooks <- s
	}))
	defer wh.Close()
	e.s.Webhook = wh.URL
	e.s.Git = fakeGit{commits: []verify.Commit{{PushedAt: t0.Add(time.Hour), Additions: 60}}}
	a := e.agent(e.user("rowan"))
	a.session("cs2", "")
	code, sub := a.submit("cs2")
	if code != 201 || sub.Status != "auto_approved" || sub.Risk != 0 || sub.VerifiedSeconds < 60*60 {
		t.Fatalf("%d %+v", code, sub)
	}
	if *sub.ApprovedSeconds != sub.VerifiedSeconds {
		t.Fatal("approved")
	}
	select {
	case got := <-hooks:
		if got.ID != sub.ID || got.Status != "auto_approved" {
			t.Fatalf("%+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no webhook")
	}
}

func TestRandomAudit(t *testing.T) {
	e := newEnv(t)
	e.s.Rand = func() float64 { return 0.01 }
	a := e.agent(e.user("rowan"))
	a.session("cs2", "")
	_, sub := a.submit("cs2")
	if sub.Status != "needs_review" || sub.Result.Flags[len(sub.Result.Flags)-1].Code != "random_audit" {
		t.Fatalf("%+v", sub)
	}
}

// A4/D1 at the server: a fabricated bundle that was never synced verifies to zero.
func TestFabricatedBundleRejected(t *testing.T) {
	e := newEnv(t)
	tok := e.user("rowan")
	honest := e.agent(tok)
	honest.session("cs2", "")
	fake := e.agent(tok)
	for i := 0; i < 60; i++ {
		fake.add(event.Event{Time: t0.Add(time.Duration(i) * time.Minute), Kind: event.Heartbeat, Project: "cs2", Repo: "github.com/kid/cs2"})
	}
	_, sub := fake.submit("cs2")
	if sub.Status != "needs_review" || sub.VerifiedSeconds != 0 {
		t.Fatalf("%+v", sub)
	}
}

// D3: copying another kid's prompts (or sharing a bot across accounts).
func TestDuplicatePromptsAcrossAccounts(t *testing.T) {
	e := newEnv(t)
	a := e.agent(e.user("rowan"))
	a.session("cs2", "")
	if _, s := a.submit("cs2"); s.Status != "auto_approved" {
		t.Fatal(s.Status)
	}
	b := e.agent(e.user("copycat"))
	b.session("cs2", "")
	_, s := b.submit("cs2")
	if s.Status != "needs_review" || !strings.Contains(fmt.Sprint(s.Result.Flags), "duplicate_prompts") {
		t.Fatalf("%+v", s.Result.Flags)
	}
	c := e.agent(e.user("original"))
	c.session("cs2", "unique")
	if _, s := c.submit("cs2"); s.Status != "auto_approved" {
		t.Fatalf("false positive %+v", s.Result.Flags)
	}
}

func TestReviewFlow(t *testing.T) {
	e := newEnv(t)
	e.s.Rand = func() float64 { return 0 } // everything goes to review
	kid := e.user("rowan")
	a := e.agent(kid)
	a.session("cs2", "")
	_, sub := a.submit("cs2")
	rtok, err := e.s.CreateUser("fraudsquad", true)
	if err != nil {
		t.Fatal(err)
	}
	other := e.user("someone")
	id := fmt.Sprint(sub.ID)

	if c, _ := e.call("GET", "/api/v1/review/queue", kid, nil); c != 403 {
		t.Fatal("kid sees queue")
	}
	if c, _ := e.call("POST", "/api/v1/submissions/"+id+"/review", kid, map[string]any{"status": "approved"}); c != 403 {
		t.Fatal("kid approves own work")
	}
	if c, _ := e.call("GET", "/api/v1/submissions/"+id, other, nil); c != 404 {
		t.Fatal("other user can read submission")
	}
	if c, b := e.call("GET", "/api/v1/submissions/"+id, kid, nil); c != 200 || !strings.Contains(string(b), "links") {
		t.Fatal("owner can't read")
	}
	_, q := e.call("GET", "/api/v1/review/queue", rtok, nil)
	var queue []Submission
	json.Unmarshal(q, &queue)
	if len(queue) != 1 || queue[0].ID != sub.ID {
		t.Fatalf("%s", q)
	}
	if c, _ := e.call("POST", "/api/v1/submissions/"+id+"/review", rtok, map[string]any{"status": "partial", "approved_seconds": sub.VerifiedSeconds + 1}); c != 400 {
		t.Fatal("over-approval")
	}
	if c, _ := e.call("POST", "/api/v1/submissions/"+id+"/review", rtok, map[string]any{"status": "lol"}); c != 400 {
		t.Fatal("bad status")
	}
	if c, _ := e.call("POST", "/api/v1/submissions/999/review", rtok, map[string]any{"status": "approved"}); c != 404 {
		t.Fatal("missing")
	}
	c, b := e.call("POST", "/api/v1/submissions/"+id+"/review", rtok, map[string]any{"status": "partial", "approved_seconds": 3600, "note": "coasting after 1h"})
	var got Submission
	json.Unmarshal(b, &got)
	if c != 200 || got.Status != "partial" || *got.ApprovedSeconds != 3600 || got.Note != "coasting after 1h" {
		t.Fatalf("%d %s", c, b)
	}
	for st, want := range map[string]int64{"approved": got.VerifiedSeconds, "rejected": 0} {
		_, b := e.call("POST", "/api/v1/submissions/"+id+"/review", rtok, map[string]any{"status": st})
		json.Unmarshal(b, &got)
		if *got.ApprovedSeconds != want {
			t.Fatal(st)
		}
	}
	if c, _ := e.call("GET", "/api/v1/submissions/"+id, rtok, nil); c != 200 {
		t.Fatal("reviewer read")
	}
	// reviewer can't review their own submission
	rv := e.agent(rtok)
	rv.dev = "device-rev01"
	rv.session("tool", "r")
	_, own := rv.submit("tool")
	if c, _ := e.call("POST", fmt.Sprintf("/api/v1/submissions/%d/review", own.ID), rtok, map[string]any{"status": "approved"}); c != 403 {
		t.Fatal("self review")
	}
}

func TestSubmitValidationAndGitErrors(t *testing.T) {
	e := newEnv(t)
	tok := e.user("rowan")
	for _, body := range []any{"x", SubmitRequest{Device: "bad"}, SubmitRequest{Device: dev, Project: "p", From: t0, To: t0}} {
		if c, _ := e.call("POST", "/api/v1/submissions", tok, body); c != 400 {
			t.Errorf("accepted %v", body)
		}
	}
	long := SubmitRequest{Device: dev, Project: "p", From: t0, To: t0.Add(400 * 24 * time.Hour), Links: []chain.Link{{}}}
	if c, _ := e.call("POST", "/api/v1/submissions", tok, long); c != 400 {
		t.Fatal("window")
	}
	e.s.Git = fakeGit{err: fmt.Errorf("rate limited")}
	a := e.agent(tok)
	a.session("cs2", "")
	if _, s := a.submit("cs2"); !strings.Contains(fmt.Sprint(s.Result.Flags), "git_unavailable") {
		t.Fatal(s.Result.Flags)
	}
}

func TestCreateUserValidation(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.CreateUser("A", false); err == nil {
		t.Fatal("bad handle")
	}
	if f := randFloat(); f < 0 || f >= 1 {
		t.Fatal(f)
	}
}

func TestHubDropsWhenPending(t *testing.T) {
	var h hub
	ch := h.subscribe(1)
	h.publish(1)
	h.publish(1) // must not block
	<-ch
	h.unsubscribe(1, ch)
	h.publish(1)
}
