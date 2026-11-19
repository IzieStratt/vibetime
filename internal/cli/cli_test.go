package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/event"
	"github.com/IzieStratt/vibetime/internal/server"
	"github.com/IzieStratt/vibetime/internal/store"
)

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestIsLoopback(t *testing.T) {
	for a, want := range map[string]bool{"127.0.0.1:1": true, "[::1]:1": true, "localhost:1": true, "0.0.0.0:1": false, "10.0.0.1:1": false, "junk": false} {
		if IsLoopback(a) != want {
			t.Errorf("%s", a)
		}
	}
}

func TestUsageAndVersion(t *testing.T) {
	var b bytes.Buffer
	if Run([]string{"wat"}, nil, &b) != 2 || Run([]string{"version"}, nil, &b) != 0 {
		t.Fatal(b.String())
	}
	if Run([]string{"serve", "-addr", "0.0.0.0:9"}, nil, &b) != 2 {
		t.Fatal("non-loopback accepted")
	}
	if Run([]string{"serve", "-bogus"}, nil, &b) != 2 {
		t.Fatal("bad flag")
	}
	if Run([]string{"hook"}, nil, &b) != 0 || Run([]string{"hook", "vim"}, strings.NewReader("{}"), &b) != 0 {
		t.Fatal("hook must always exit 0")
	}
}

// End to end: real serve loop + hook subcommand, as a harness would invoke it.
func TestServeAndHookE2E(t *testing.T) {
	home := t.TempDir()
	addr := freeAddr(t)
	t.Setenv("VIBETIME_HOME", home)
	t.Setenv("VIBETIME_URL", "http://"+addr)

	// hook while agent is down → spooled, exit 0
	var errb bytes.Buffer
	cwd := filepath.Join(home, "proj")
	os.MkdirAll(cwd, 0o755)
	payload := fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","cwd":%q,"prompt":"hi"}`, cwd)
	if Run([]string{"hook", "claude-code"}, strings.NewReader(payload), &errb) != 0 {
		t.Fatal(errb.String())
	}
	if _, err := os.Stat(filepath.Join(home, "spool.jsonl")); err != nil {
		t.Fatal("expected spool")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- RunContext(ctx, []string{"serve", "-addr", addr}, nil, &bytes.Buffer{}) }()
	t.Cleanup(func() {
		cancel()
		if code := <-done; code != 0 {
			t.Errorf("serve exit %d", code)
		}
	})
	for i := 0; i < 50; i++ {
		if r, err := http.Get("http://" + addr + "/v1/health"); err == nil {
			r.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Codex passes JSON as last argv
	codex := fmt.Sprintf(`{"type":"agent-turn-complete","cwd":%q,"input-messages":["x"]}`, cwd)
	Run([]string{"hook", "codex", codex}, nil, &errb)

	r, err := http.Get("http://" + addr + "/v1/summary")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var m struct{ Head string }
	json.NewDecoder(r.Body).Decode(&m)
	if _, err := os.Stat(filepath.Join(home, "spool.jsonl")); !os.IsNotExist(err) {
		t.Fatal("spool not flushed")
	}
	if len(m.Head) != 64 {
		t.Fatalf("head %q", m.Head)
	}
}

func TestServeBadDB(t *testing.T) {
	var b bytes.Buffer
	f := filepath.Join(t.TempDir(), "file")
	os.WriteFile(f, nil, 0o600)
	if Run([]string{"serve", "-addr", freeAddr(t), "-db", filepath.Join(f, "sub", "x.db")}, nil, &b) != 1 {
		t.Fatal("expected failure for unusable db path")
	}
}

func TestServePortInUse(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	var b bytes.Buffer
	if Run([]string{"serve", "-addr", l.Addr().String(), "-db", filepath.Join(t.TempDir(), "x.db")}, nil, &b) != 1 {
		t.Fatal("expected failure when port busy")
	}
}

func TestLoginSubmitCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIBETIME_HOME", home)
	srv, err := server.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv.OpenSignup = true
	ts := httptest.NewServer(srv.Handler())
	defer func() { ts.Close(); srv.Close() }()
	var b bytes.Buffer
	for _, bad := range [][]string{{"login"}, {"login", "-server", ts.URL}, {"login", "-server", ts.URL, "-handle", "a", "-token", "b"}} {
		if Run(bad, nil, &b) != 2 {
			t.Errorf("%v accepted", bad)
		}
	}
	if Run([]string{"submit", "-project", "p", "-from", "2026-01-01T00:00:00Z"}, nil, &b) != 1 {
		t.Fatal("submit before login")
	}
	if Run([]string{"login", "-server", "http://127.0.0.1:1", "-handle", "rowan"}, nil, &b) != 1 {
		t.Fatal("unreachable server")
	}
	if Run([]string{"login", "-server", ts.URL, "-handle", "rowan"}, nil, &b) != 0 {
		t.Fatal(b.String())
	}
	if Run([]string{"submit"}, nil, &b) != 2 || Run([]string{"submit", "-project", "p", "-from", "yesterday"}, nil, &b) != 2 {
		t.Fatal("submit usage")
	}
	// a real store with a few events, submitted for review
	st, _ := store.Open(filepath.Join(home, "events.db"))
	now := time.Now().UTC().Truncate(time.Second)
	st.Append(event.Event{Time: now.Add(-3 * time.Minute), Kind: event.Prompt, Project: "cs2", PromptText: "hello there"})
	st.Append(event.Event{Time: now.Add(-2 * time.Minute), Kind: event.Heartbeat, Project: "cs2"})
	st.Close()
	b.Reset()
	if Run([]string{"submit", "-project", "cs2", "-repo", "https://github.com/rowan/cs2.git", "-from", now.Add(-time.Hour).Format(time.RFC3339)}, nil, &b) != 0 {
		t.Fatal(b.String())
	}
	if !strings.Contains(b.String(), "submission 1") || !strings.Contains(b.String(), "unanchored") {
		t.Fatalf("expected an unanchored (never synced) submission: %s", b.String())
	}
	if Run([]string{"submit", "-project", "nope", "-from", now.Add(time.Hour).Format(time.RFC3339)}, nil, &b) != 1 {
		t.Fatal("empty window")
	}
	if Run([]string{"login", "-server", ts.URL, "-token", "vt_x"}, nil, &b) != 0 {
		t.Fatal("token login")
	}
	if Run([]string{"submit", "-project", "p", "-from", now.Format(time.RFC3339), "-db", filepath.Join(home, "missing", "x", "y.db")}, nil, &b) != 1 {
		t.Fatal("bad db")
	}
}
