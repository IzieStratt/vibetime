package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	go Run([]string{"serve", "-addr", addr}, nil, &bytes.Buffer{})
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
