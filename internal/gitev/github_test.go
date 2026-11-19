package gitev

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCommits(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/repos/kid/cs2/activity":
			w.Write([]byte(`[
			 {"before":"aaa","after":"bbb","timestamp":"2026-11-18T16:00:00Z","activity_type":"push"},
			 {"before":"0000","after":"ccc","timestamp":"2026-11-18T17:00:00Z","activity_type":"branch_creation"},
			 {"before":"x","after":"y","timestamp":"2026-11-18T18:00:00Z","activity_type":"pr_merge"},
			 {"before":"x","after":"z","timestamp":"2026-11-10T00:00:00Z","activity_type":"push"}]`))
		case "/repos/kid/cs2/compare/aaa...bbb":
			w.Write([]byte(`{"files":[{"additions":10,"deletions":2},{"additions":5,"deletions":0}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer ts.Close()
	g := &GitHub{BaseURL: ts.URL, Token: "tok"}
	from, to := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	cs, err := g.Commits("github.com/kid/cs2", from, to)
	if err != nil || len(cs) != 2 || cs[0].Additions != 15 || cs[0].Deletions != 2 || cs[1].SHA != "ccc" {
		t.Fatalf("%+v %v", cs, err)
	}
	if _, err := g.Commits("gitlab.com/a/b", from, to); err != ErrUnsupported {
		t.Fatal(err)
	}
	if _, err := (&GitHub{BaseURL: ts.URL}).Commits("github.com/kid/cs2", from, to); err == nil {
		t.Fatal("expected 401 error")
	}
	if _, err := (&GitHub{BaseURL: "http://127.0.0.1:1"}).Commits("github.com/a/b", from, to); err == nil {
		t.Fatal("expected network error")
	}
	if _, err := (&GitHub{BaseURL: ts.URL, Token: "tok"}).Commits("github.com/kid/missing", from, to); err == nil {
		t.Fatal("expected 404")
	}
}

func TestRejectsPathTricks(t *testing.T) {
	g := &GitHub{BaseURL: "http://127.0.0.1:1", Token: "secret"}
	for _, r := range []string{"github.com/../user", "github.com/a/b?x=1", "github.com/a/b#c", "github.com/a/%2e%2e", "github.com/a/.."} {
		if _, err := g.Commits(r, time.Now(), time.Now()); err != ErrBadRepo {
			t.Errorf("%s: %v", r, err)
		}
	}
}

func TestPagination(t *testing.T) {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var pages []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("page")
		pages = append(pages, p)
		var out []string
		n := 100
		if p == "3" {
			n = 5
		}
		for i := 0; i < n; i++ {
			// newest first: page 1 = Nov 20.., page 2 = Nov 10.., page 3 = Oct (before window)
			day := map[string]string{"1": "2026-11-20", "2": "2026-11-10", "3": "2026-10-20"}[p]
			out = append(out, `{"before":"","after":"x","timestamp":"`+day+`T00:00:00Z","activity_type":"push"}`)
		}
		w.Write([]byte("[" + strings.Join(out, ",") + "]"))
	}))
	defer ts.Close()
	cs, err := (&GitHub{BaseURL: ts.URL}).Commits("github.com/kid/cs2", from, from.AddDate(0, 1, 0))
	if err != nil || len(cs) != 200 || strings.Join(pages, ",") != "1,2,3" {
		t.Fatalf("%d %v %v", len(cs), pages, err)
	}
}
