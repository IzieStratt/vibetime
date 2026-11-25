package api

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/control"
	"github.com/IzieStratt/vibetime/internal/store"
)

// controlled starts an agent handler with a control store in its own data dir.
func controlled(t *testing.T) (*httptest.Server, *store.Store, *control.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctl := &control.Store{Dir: t.TempDir()}
	ts := httptest.NewServer((&Server{Store: s, Now: func() time.Time { return now }, MaxSkew: time.Hour, Control: ctl}).Handler())
	t.Cleanup(ts.Close)
	return ts, s, ctl
}

func TestExcludedFolderEventsAreDropped(t *testing.T) {
	ts, s, ctl := controlled(t)
	secret := t.TempDir()
	if _, err := ctl.Exclude(secret); err != nil {
		t.Fatal(err)
	}
	ts1 := now.Format(time.RFC3339)
	body := fmt.Sprintf(`[
		{"time":%q,"kind":"prompt","project":"s","prompt_text":"inside the secret folder","cwd":%q},
		{"time":%q,"kind":"prompt","project":"s","prompt_text":"deeper inside the folder now","cwd":%q},
		{"time":%q,"kind":"edit","project":"open","human":true,"lines_added":2,"cwd":"/elsewhere/open"}
	]`, ts1, secret, ts1, filepath.Join(secret, "sub"), ts1)
	r := post(t, ts.URL+"/v1/events", body)
	defer r.Body.Close()
	if r.StatusCode != 202 {
		t.Fatalf("status %d", r.StatusCode)
	}
	evs, _ := s.Range(now.Add(-time.Minute), now.Add(time.Minute))
	if len(evs) != 1 || evs[0].Project != "open" {
		t.Fatalf("only the non-excluded event should be stored, got %+v", evs)
	}
}

func TestPausedDropsEverythingUntilResumed(t *testing.T) {
	ts, s, ctl := controlled(t)
	if err := ctl.Pause(now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ev := fmt.Sprintf(`[{"time":%q,"kind":"prompt","project":"p","prompt_text":"a long enough prompt to keep"}]`, now.Format(time.RFC3339))
	post(t, ts.URL+"/v1/events", ev).Body.Close()
	hb := fmt.Sprintf(`{"time":%d,"project":"p","language":"Go"}`, now.Unix())
	r := post(t, ts.URL+"/api/v1/users/current/heartbeats", hb)
	r.Body.Close()
	if r.StatusCode != 202 {
		t.Fatalf("paused heartbeat should look like success to the editor, got %d", r.StatusCode)
	}
	if evs, _ := s.Range(now.Add(-time.Minute), now.Add(time.Minute)); len(evs) != 0 {
		t.Fatalf("paused agent stored %d events", len(evs))
	}
	if err := ctl.Resume(); err != nil {
		t.Fatal(err)
	}
	post(t, ts.URL+"/v1/events", ev).Body.Close()
	if evs, _ := s.Range(now.Add(-time.Minute), now.Add(time.Minute)); len(evs) != 1 {
		t.Fatalf("after resume the event should be stored, got %d", len(evs))
	}
}

func TestPauseExpires(t *testing.T) {
	ts, s, ctl := controlled(t)
	if err := ctl.Pause(now.Add(-time.Second)); err != nil { // already over
		t.Fatal(err)
	}
	ev := fmt.Sprintf(`[{"time":%q,"kind":"prompt","project":"p","prompt_text":"a long enough prompt to keep"}]`, now.Format(time.RFC3339))
	post(t, ts.URL+"/v1/events", ev).Body.Close()
	if evs, _ := s.Range(now.Add(-time.Minute), now.Add(time.Minute)); len(evs) != 1 {
		t.Fatal("an expired pause must not drop events")
	}
}

// receiver records forwarded heartbeat bodies.
func receiver(t *testing.T, delay time.Duration) (*httptest.Server, chan [2]string) {
	got := make(chan [2]string, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- [2]string{string(b), r.Header.Get("Authorization")}
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
			}
		}
		w.WriteHeader(201)
	}))
	t.Cleanup(ts.Close)
	return ts, got
}

func TestForwardingSendsRawBodyWithKey(t *testing.T) {
	ts, s, ctl := controlled(t)
	hk, got := receiver(t, 0)
	if err := ctl.SetForward(hk.URL+"/api/v1/users/current/heartbeats", "secret-key"); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`[{"time":%d,"project":"p","language":"Go","lines":3}]`, now.Unix())
	r := post(t, ts.URL+"/api/v1/users/current/heartbeats", body)
	r.Body.Close()
	if r.StatusCode != 202 {
		t.Fatalf("local response %d", r.StatusCode)
	}
	select {
	case g := <-got:
		if g[0] != body {
			t.Errorf("forwarded body %q, want the raw body %q", g[0], body)
		}
		if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("secret-key")); g[1] != want {
			t.Errorf("auth header %q, want %q", g[1], want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat was not forwarded")
	}
	if evs, _ := s.Range(now.Add(-time.Minute), now.Add(time.Minute)); len(evs) != 1 {
		t.Errorf("local recording must still happen, got %d events", len(evs))
	}
}

func TestForwardingIsOffByDefault(t *testing.T) {
	ts, _, _ := controlled(t)
	hk, got := receiver(t, 0)
	_ = hk // no forward_url is configured, so nothing may reach hk
	post(t, ts.URL+"/api/v1/users/current/heartbeats", fmt.Sprintf(`{"time":%d,"project":"p"}`, now.Unix())).Body.Close()
	select {
	case g := <-got:
		t.Fatalf("unconfigured forwarder sent %q", g[0])
	case <-time.After(200 * time.Millisecond):
	}
}

func TestForwardingNeverDelaysTheLocalResponse(t *testing.T) {
	ts, s, ctl := controlled(t)
	// The endpoint hangs longer than the forwarder's 2s budget.
	hk, _ := receiver(t, 4*time.Second)
	if err := ctl.SetForward(hk.URL, ""); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r := post(t, ts.URL+"/api/v1/users/current/heartbeats", fmt.Sprintf(`{"time":%d,"project":"p"}`, now.Unix()))
	r.Body.Close()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("local response took %v; forwarding must not block it", d)
	}
	if r.StatusCode != 202 {
		t.Fatalf("local response %d", r.StatusCode)
	}
	if evs, _ := s.Range(now.Add(-time.Minute), now.Add(time.Minute)); len(evs) != 1 {
		t.Fatalf("local recording lost, got %d", len(evs))
	}
}

func TestForwardingUnreachableEndpointIsHarmless(t *testing.T) {
	ts, s, ctl := controlled(t)
	if err := ctl.SetForward("http://127.0.0.1:1/unused", ""); err != nil {
		t.Fatal(err)
	}
	r := post(t, ts.URL+"/api/v1/users/current/heartbeats", fmt.Sprintf(`{"time":%d,"project":"p"}`, now.Unix()))
	r.Body.Close()
	if r.StatusCode != 202 {
		t.Fatalf("status %d", r.StatusCode)
	}
	if evs, _ := s.Range(now.Add(-time.Minute), now.Add(time.Minute)); len(evs) != 1 {
		t.Fatal("unreachable forward target must not affect local recording")
	}
}

func TestNoForwardWhilePaused(t *testing.T) {
	ts, _, ctl := controlled(t)
	hk, got := receiver(t, 0)
	ctl.SetForward(hk.URL, "")
	ctl.Pause(now.Add(time.Hour))
	post(t, ts.URL+"/api/v1/users/current/heartbeats", fmt.Sprintf(`{"time":%d,"project":"p"}`, now.Unix())).Body.Close()
	select {
	case <-got:
		t.Fatal("paused agent must not forward")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestCwdNeverReachesChainOrSummary(t *testing.T) {
	ts, s, _ := controlled(t)
	body := fmt.Sprintf(`[{"time":%q,"kind":"prompt","project":"p","prompt_text":"a long enough prompt to keep","relevance_pm":900,"cwd":"/Users/someone/private/app"}]`, now.Format(time.RFC3339))
	post(t, ts.URL+"/v1/events", body).Body.Close()
	links, err := s.Links()
	if err != nil || len(links) != 1 {
		t.Fatalf("links %v %v", links, err)
	}
	for _, l := range links {
		if l.Event.Cwd != "" {
			t.Fatal("cwd in chain")
		}
	}
	evs, _ := s.Range(now.Add(-time.Minute), now.Add(time.Minute))
	raw := fmt.Sprintf("%+v", evs)
	if strings.Contains(raw, "private/app") {
		t.Fatal("cwd value present in stored events")
	}
	if len(evs) != 1 || evs[0].RelevancePM == nil || *evs[0].RelevancePM != 900 {
		t.Fatal("the relevance score itself should still be stored")
	}
}

func TestPresenceGateSeesExcludedFolderAsLatest(t *testing.T) {
	// The agent withholds presence when the latest event's folder is excluded. Events from an
	// excluded folder are dropped, but the folder must still become the "latest" one, otherwise
	// presence while working there would be recorded against the previous, open folder.
	ts, _, ctl := controlled(t)
	secret := t.TempDir()
	if _, err := ctl.Exclude(secret); err != nil {
		t.Fatal(err)
	}
	ts1 := now.Format(time.RFC3339)
	body := fmt.Sprintf(`[
		{"time":%q,"kind":"edit","project":"open","human":true,"lines_added":2,"cwd":"/elsewhere/open"},
		{"time":%q,"kind":"prompt","project":"s","prompt_text":"inside the secret folder","cwd":%q}
	]`, ts1, ts1, secret)
	r := post(t, ts.URL+"/v1/events", body)
	r.Body.Close()
	cfg, _ := ctl.Load()
	if ctl.LastCwd() != secret || !cfg.Excludes(ctl.LastCwd()) {
		t.Fatalf("latest folder = %q, want the excluded %q", ctl.LastCwd(), secret)
	}
}

func TestExcludedHeartbeatsAreNeitherStoredNorForwarded(t *testing.T) {
	ts, s, ctl := controlled(t)
	hk, got := receiver(t, 0)
	if err := ctl.SetForward(hk.URL+"/hb", ""); err != nil {
		t.Fatal(err)
	}
	secret := t.TempDir()
	if _, err := ctl.Exclude(secret); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`[{"time":%d,"project":"secret","entity":%q,"language":"Go"},{"time":%d,"project":"open","entity":"/work/open/a.go","language":"Go"}]`,
		now.Unix(), secret+"/main.go", now.Unix())
	r := post(t, ts.URL+"/api/v1/users/current/heartbeats", body)
	r.Body.Close()
	if r.StatusCode != 202 {
		t.Fatalf("status %d", r.StatusCode)
	}
	select {
	case g := <-got:
		if strings.Contains(g[0], "secret") || !strings.Contains(g[0], "open") {
			t.Fatalf("forwarded body must hold only the non-excluded heartbeat, got %s", g[0])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("non-excluded heartbeat was not forwarded")
	}
	evs, _ := s.Range(now.Add(-time.Minute), now.Add(time.Minute))
	if len(evs) != 1 || evs[0].Project != "open" {
		t.Fatalf("stored %+v", evs)
	}
	// a batch made only of excluded heartbeats forwards nothing
	only := fmt.Sprintf(`[{"time":%d,"project":"secret","entity":%q}]`, now.Unix(), secret+"/x.go")
	post(t, ts.URL+"/api/v1/users/current/heartbeats", only).Body.Close()
	select {
	case g := <-got:
		t.Fatalf("excluded-only batch leaked: %s", g[0])
	case <-time.After(300 * time.Millisecond):
	}
}
