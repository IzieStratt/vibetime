package syncer

import (
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

func TestVerify(t *testing.T) {
	_, srv, _, _ := setup(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	if _, err := Verify(ts.URL, "vt_nope", nil); err == nil {
		t.Fatal("bogus token accepted")
	}
	tok, err := srv.CreateUser("wren", false)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Verify(ts.URL, tok, nil)
	if err != nil || h != "wren" {
		t.Fatalf("verify real token: handle=%q err=%v", h, err)
	}
}
