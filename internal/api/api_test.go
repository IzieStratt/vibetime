package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/store"
)

var now = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func srv(t *testing.T) *httptest.Server {
	s, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ts := httptest.NewServer((&Server{Store: s, Now: func() time.Time { return now }}).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func post(t *testing.T, url, body string) *http.Response {
	r, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func summary(t *testing.T, base string) map[string]any {
	r, err := http.Get(base + "/v1/summary")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var m map[string]any
	json.NewDecoder(r.Body).Decode(&m)
	return m
}

func TestEventsToSummary(t *testing.T) {
	ts := srv(t)
	var evs []string
	for i := 0; i <= 10; i++ {
		evs = append(evs, fmt.Sprintf(`{"time":%q,"kind":"prompt","project":"cs2","harness":"claude-code","prompt_text":"SECRET PROMPT"}`,
			now.Add(time.Duration(i-30)*time.Minute).Format(time.RFC3339)))
	}
	if r := post(t, ts.URL+"/v1/events", "["+strings.Join(evs, ",")+"]"); r.StatusCode != 202 {
		t.Fatalf("status %d", r.StatusCode)
	}
	m := summary(t, ts.URL)
	if m["total_seconds"].(float64) != 600 {
		t.Fatalf("got %v", m)
	}
	// privacy: aggregate output must not contain prompt content
	b, _ := json.Marshal(m)
	if strings.Contains(string(b), "SECRET") {
		t.Fatal("prompt text leaked into summary")
	}
}

func TestEventsRejectsBad(t *testing.T) {
	ts := srv(t)
	for _, body := range []string{`not json`, `[{"kind":"prompt"}]`, `[{"time":"2026-01-01T00:00:00Z","kind":"nope","project":"p"}]`} {
		if r := post(t, ts.URL+"/v1/events", body); r.StatusCode != 400 {
			t.Errorf("%s: status %d", body, r.StatusCode)
		}
	}
}

func TestWakaTimeCompat(t *testing.T) {
	ts := srv(t)
	base := float64(now.Add(-10 * time.Minute).Unix())
	single := fmt.Sprintf(`{"time":%f,"project":"site","language":"Go","entity":"/x.go","type":"file"}`, base)
	if r := post(t, ts.URL+"/api/v1/users/current/heartbeats", single); r.StatusCode != 202 {
		t.Fatalf("single %d", r.StatusCode)
	}
	bulk := fmt.Sprintf(`[{"time":%f,"project":"site"},{"time":%f,"project":"site"}]`, base+60, base+120)
	if r := post(t, ts.URL+"/api/v1/users/current/heartbeats.bulk", bulk); r.StatusCode != 202 {
		t.Fatalf("bulk %d", r.StatusCode)
	}
	m := summary(t, ts.URL)
	if m["projects"].(map[string]any)["site"].(float64) != 120 {
		t.Fatalf("got %v", m)
	}
}

func TestSummaryBadHours(t *testing.T) {
	ts := srv(t)
	for _, q := range []string{"x", "0", "-1", "999999"} {
		r, _ := http.Get(ts.URL + "/v1/summary?hours=" + q)
		if r.StatusCode != 400 {
			t.Errorf("hours=%s: %d", q, r.StatusCode)
		}
	}
}

func TestBodyLimit(t *testing.T) {
	ts := srv(t)
	big := "[" + strings.Repeat(" ", 2<<20) + "]"
	if r := post(t, ts.URL+"/v1/events", big); r.StatusCode != 400 {
		t.Fatalf("status %d", r.StatusCode)
	}
}

func FuzzEvents(f *testing.F) {
	f.Add(`[{"time":"2026-01-01T00:00:00Z","kind":"prompt","project":"p"}]`)
	f.Add(`{"time":1.5}`)
	f.Fuzz(func(t *testing.T, body string) {
		s, err := store.Open(filepath.Join(t.TempDir(), "f.db"))
		if err != nil {
			t.Skip()
		}
		defer s.Close()
		h := (&Server{Store: s, Now: time.Now}).Handler()
		for _, p := range []string{"/v1/events", "/api/v1/users/current/heartbeats"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", p, strings.NewReader(body)))
			if rec.Code >= 500 {
				t.Fatalf("%s 5xx on %q", p, body)
			}
		}
	})
}
