package syncer

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/event"
	"github.com/IzieStratt/vibetime/internal/server"
	"github.com/IzieStratt/vibetime/internal/store"
)

var t0 = time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

func setup(t *testing.T) (*Syncer, *server.Server, *clock, string) {
	clk := &clock{t: t0}
	srv, err := server.Open(filepath.Join(t.TempDir(), "srv.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv.Now, srv.OpenSignup, srv.Rand = clk.now, true, func() float64 { return 0.99 }
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Close() })
	dir := t.TempDir()
	cfg, err := Login(dir, ts.URL, "rowan", nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Syncer{Store: st, Cfg: cfg, Now: clk.now}, srv, clk, dir
}

// Full loop: a real agent store, live sync every minute, then a submission.
func TestLiveSyncThenSubmit(t *testing.T) {
	sy, srv, clk, _ := setup(t)
	const repo = "github.com/rowan/cs2"
	gaps := []int{3, 5, 4, 3, 6}
	m := t0
	var events []event.Event
	for i := 0; i < 20; i++ {
		if i > 0 {
			m = m.Add(time.Duration(gaps[i%5]) * time.Minute)
		}
		events = append(events,
			event.Event{Time: m, Kind: event.Prompt, Project: "cs2", Repo: repo, PromptText: fmt.Sprintf("SECRET make the knife inspect animation smoother part %d", i)},
			event.Event{Time: m.Add(30 * time.Second), Kind: event.Edit, Project: "cs2", Repo: repo, LinesAdded: 3},
			event.Event{Time: m.Add(time.Minute), Kind: event.Heartbeat, Project: "cs2", Repo: repo},
			event.Event{Time: m.Add(90 * time.Second), Kind: event.Heartbeat, Project: "homework"},
			event.Event{Time: m.Add(2 * time.Minute), Kind: event.Heartbeat, Project: "cs2", Repo: repo},
		)
		if i%8 == 7 {
			events = append(events, event.Event{Time: m.Add(150 * time.Second), Kind: event.Idle})
		}
	}
	// replay in real time: append events as they happen, sync once a minute
	next := t0.Add(time.Minute)
	for _, e := range events {
		for !next.After(e.Time) {
			clk.set(next)
			if err := sy.Tick(); err != nil {
				t.Fatal(err)
			}
			next = next.Add(time.Minute)
		}
		clk.set(e.Time)
		if _, err := sy.Store.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	clk.set(next)
	sy.Tick()

	st, _ := srv.UserStats(struct {
		ID       int64
		Handle   string
		Reviewer bool
		Public   bool
	}{1, "rowan", false, true})
	if st.Projects["cs2"] < 30*60 || st.Projects["homework"] == 0 {
		t.Fatalf("live stats %+v", st)
	}
	in, err := srv.UserInsights(struct {
		ID       int64
		Handle   string
		Reviewer bool
		Public   bool
	}{1, "rowan", false, true}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	modes := in.HandsOn + in.Steering + in.Watching
	var total int64
	for _, d := range in.Days {
		total += d.Seconds
	}
	if in.HandsOn == 0 || in.Steering == 0 || modes < total-int64(len(in.Projects))*60 || modes > total+60 {
		t.Fatalf("modes %d/%d/%d sum %d vs total %d", in.HandsOn, in.Steering, in.Watching, modes, total)
	}
	if in.Prompts != 20 || in.AILines != 60 {
		t.Fatalf("steering counts %+v", in)
	}

	sub, err := sy.Submit("cs2", repo, t0, m.Add(3*time.Minute), false)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Status != "auto_approved" || sub.VerifiedSeconds < 30*60 {
		t.Fatalf("%+v", sub)
	}
	// privacy: prompt text never left the machine
	var leaked bool
	srv.DB.QueryRow(`SELECT bundle LIKE '%SECRET%' FROM submissions WHERE id = ?`, sub.ID).Scan(&leaked)
	if leaked {
		t.Fatal("prompt text uploaded without consent")
	}
	// opting in includes this project's prompts only
	sub2, err := sy.Submit("cs2", repo, t0, m.Add(3*time.Minute), true)
	if err != nil {
		t.Fatal(err)
	}
	srv.DB.QueryRow(`SELECT bundle LIKE '%SECRET%' FROM submissions WHERE id = ?`, sub2.ID).Scan(&leaked)
	if !leaked {
		t.Fatal("opt-in prompts missing")
	}
}

func TestSubmitErrors(t *testing.T) {
	sy, _, _, _ := setup(t)
	if _, err := sy.Submit("cs2", "", t0, t0.Add(time.Hour), false); err == nil || !strings.Contains(err.Error(), "no events") {
		t.Fatal(err)
	}
	sy.Store.Append(event.Event{Time: t0, Kind: event.Heartbeat, Project: "cs2"})
	sy.Cfg.Token = "bad"
	if _, err := sy.Submit("cs2", "", t0, t0.Add(time.Hour), false); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatal(err)
	}
	sy.Cfg.Server = "http://127.0.0.1:1"
	if err := sy.Tick(); err == nil {
		t.Fatal("expected network error")
	}
}

func TestRunStopsAndReportsErrors(t *testing.T) {
	sy, _, _, _ := setup(t)
	sy.Cfg.Server = "http://127.0.0.1:1"
	stop := make(chan struct{})
	errs := make(chan error, 10)
	done := make(chan struct{})
	go func() { sy.Run(5*time.Millisecond, stop, func(e error) { errs <- e }); close(done) }()
	<-errs
	close(stop)
	<-done
}

func TestConfigAndLogin(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadConfig(dir); err == nil {
		t.Fatal("missing config")
	}
	SaveConfig(dir, Config{Server: "s", Token: "t"})
	c, err := LoadConfig(dir)
	if err != nil || c.Device == "" || !strings.HasPrefix(c.Device, "dev-") {
		t.Fatal(c, err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "config.json")); fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatal("config readable by others")
	}
	if _, err := Login(dir, "http://127.0.0.1:1", "x", nil); err == nil {
		t.Fatal("network")
	}
	_, srv, _, _ := setup(t)
	srv.OpenSignup = false
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	if _, err := Login(t.TempDir(), ts.URL, "someone", nil); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "file")
	os.WriteFile(f, nil, 0o600)
	if err := SaveConfig(filepath.Join(f, "sub"), Config{}); err == nil {
		t.Fatal("unwritable dir")
	}
}
