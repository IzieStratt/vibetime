package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/store"
)

func guarded(t *testing.T) (http.Handler, *store.Store) {
	s, err := store.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return (&Server{Store: s, Now: func() time.Time { return now }}).Handler(), s
}

func do(h http.Handler, method, host, origin, ctype, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+host+"/v1/events", strings.NewReader(body))
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func evAt(d time.Duration) string {
	return `[{"time":"` + now.Add(d).Format(time.RFC3339) + `","kind":"prompt","project":"p"}]`
}

func TestGuardBlocksBrowserAttacks(t *testing.T) {
	h, _ := guarded(t)
	body := evAt(0)
	cases := []struct {
		name, host, origin, ctype string
		want                      int
	}{
		{"ok plugin", "127.0.0.1:47391", "", "application/json", 202},
		{"ok localhost", "localhost:47391", "", "application/json; charset=utf-8", 202},
		{"ok ipv6", "[::1]:47391", "", "application/json", 202},
		{"ok loopback origin", "127.0.0.1:47391", "http://localhost:3000", "application/json", 202},
		{"dns rebinding", "evil.example:47391", "", "application/json", 403},
		{"cross-site origin", "127.0.0.1:47391", "https://evil.example", "application/json", 403},
		{"garbage origin", "127.0.0.1:47391", "::::", "application/json", 403},
		{"form csrf (simple request)", "127.0.0.1:47391", "", "text/plain", 415},
		{"no content type", "127.0.0.1:47391", "", "", 415},
	}
	for _, c := range cases {
		if w := do(h, "POST", c.host, c.origin, c.ctype, body); w.Code != c.want {
			t.Errorf("%s: got %d want %d", c.name, w.Code, c.want)
		}
	}
	if w := do(h, "GET", "evil.example", "", "", ""); w.Code != 403 {
		t.Errorf("rebinding GET: %d", w.Code)
	}
}

func TestStaleAndFutureEventsDropped(t *testing.T) {
	h, s := guarded(t)
	for d, accept := range map[time.Duration]bool{
		0: true, -4 * time.Minute: true, 30 * time.Second: true,
		-6 * time.Minute: false, -30 * 24 * time.Hour: false, 2 * time.Minute: false,
	} {
		w := do(h, "POST", "127.0.0.1:1", "", "application/json", evAt(d))
		var m map[string]any
		json.Unmarshal(w.Body.Bytes(), &m)
		if (m["accepted"] == 1.0) != accept {
			t.Errorf("offset %v: %v", d, m)
		}
	}
	links, _ := s.Links()
	if len(links) != 3 {
		t.Fatalf("stored %d", len(links))
	}
}

func TestBackfillViaWakaTimeBlocked(t *testing.T) {
	h, s := guarded(t)
	r := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/users/current/heartbeats",
		strings.NewReader(`{"time":`+jsonNum(now.Add(-48*time.Hour))+`,"project":"p"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if links, _ := s.Links(); len(links) != 0 || !strings.Contains(w.Body.String(), "stale") {
		t.Fatalf("backfilled heartbeat accepted: %s", w.Body.String())
	}
}

func jsonNum(t time.Time) string { b, _ := json.Marshal(t.Unix()); return string(b) }

func TestInvalidBatchStoresNothing(t *testing.T) {
	h, s := guarded(t)
	body := `[` + strings.Trim(evAt(0), "[]") + `,{"kind":"bogus"}]`
	if w := do(h, "POST", "127.0.0.1:1", "", "application/json", body); w.Code != 400 {
		t.Fatalf("code %d", w.Code)
	}
	if links, _ := s.Links(); len(links) != 0 {
		t.Fatal("partial batch stored")
	}
}
