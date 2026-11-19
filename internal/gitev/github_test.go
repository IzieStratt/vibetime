package gitev

import (
	"net/http"
	"net/http/httptest"
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
