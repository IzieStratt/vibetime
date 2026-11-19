package syncer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fake Hackatime: same response shape as WakatimeService#generate_daily_summaries
func fakeHackatime(t *testing.T, calls *[]string) *httptest.Server {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer hk_key" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path != "/users/current/summaries" {
			w.WriteHeader(404)
			return
		}
		s, e := r.URL.Query().Get("start"), r.URL.Query().Get("end")
		*calls = append(*calls, s+".."+e)
		sd, _ := time.Parse("2006-01-02", s)
		ed, _ := time.Parse("2006-01-02", e)
		if ed.Sub(sd) >= 366*24*time.Hour {
			w.WriteHeader(400)
			return
		}
		var data []map[string]any
		for d := sd; !d.After(ed); d = d.AddDate(0, 0, 1) {
			data = append(data, map[string]any{"range": map[string]any{"date": d.Format("2006-01-02")},
				"projects": []map[string]any{{"name": "cs2", "total_seconds": 3600.7}, {"name": "zero", "total_seconds": 0}}})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestImportHackatime(t *testing.T) {
	sy, srv, clk, _ := setup(t)
	var calls []string
	hk := fakeHackatime(t, &calls)
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) // > 366 days: must be chunked
	n, err := sy.ImportHackatime(hk.URL, "hk_key", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || n != 425 {
		t.Fatalf("calls %v imported %d", calls, n)
	}
	// re-import is idempotent
	if n2, _ := sy.ImportHackatime(hk.URL, "hk_key", from, to); n2 != 425 {
		t.Fatal(n2)
	}
	var total int64
	srv.DB.QueryRow(`SELECT SUM(seconds) FROM imported`).Scan(&total)
	if total != 425*3600 {
		t.Fatal(total)
	}
	// imported time never shows up in verified stats
	clk.set(to)
	st, _ := srv.UserStats(struct {
		ID       int64
		Handle   string
		Reviewer bool
		Public   bool
	}{1, "rowan", false, true})
	if st.WeekSeconds != 0 || len(st.Projects) != 0 {
		t.Fatalf("imported time leaked into verified stats: %+v", st)
	}
	if _, err := sy.ImportHackatime(hk.URL, "wrong", from, to); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatal(err)
	}
	if _, err := FetchHackatime("http://127.0.0.1:1", "k", from, from, nil); err == nil {
		t.Fatal("network")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "not json") }))
	defer bad.Close()
	if _, err := FetchHackatime(bad.URL, "k", from, from, nil); err == nil {
		t.Fatal("bad json")
	}
	sy.Cfg.Token = "nope"
	if _, err := sy.ImportHackatime(hk.URL, "hk_key", from, from); err == nil {
		t.Fatal("server auth")
	}
}
