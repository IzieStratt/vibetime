// Package cli implements the vibetime command line.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IzieStratt/vibetime/internal/api"
	"github.com/IzieStratt/vibetime/internal/client"
	"github.com/IzieStratt/vibetime/internal/event"
	"github.com/IzieStratt/vibetime/internal/hooks"
	"github.com/IzieStratt/vibetime/internal/presence"
	"github.com/IzieStratt/vibetime/internal/store"
)

const DefaultAddr = "127.0.0.1:47391"

func dataDir() string {
	if d := os.Getenv("VIBETIME_HOME"); d != "" {
		return d
	}
	d, _ := os.UserConfigDir()
	return filepath.Join(d, "vibetime")
}

func agentURL() string {
	if u := os.Getenv("VIBETIME_URL"); u != "" {
		return u
	}
	return "http://" + DefaultAddr
}

// Run executes the CLI and returns an exit code.
func Run(args []string, stdin io.Reader, stderr io.Writer) int {
	if len(args) == 0 {
		args = []string{"serve"}
	}
	switch args[0] {
	case "serve":
		return serve(args[1:], stderr)
	case "hook":
		return hook(args[1:], stdin, stderr)
	case "version":
		fmt.Fprintln(stderr, "vibetime 0.1.0")
		return 0
	}
	fmt.Fprintf(stderr, "usage: vibetime [serve|hook <harness>|version]\n")
	return 2
}

func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func serve(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", DefaultAddr, "loopback listen address")
	db := fs.String("db", filepath.Join(dataDir(), "events.db"), "SQLite path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !IsLoopback(*addr) {
		fmt.Fprintf(stderr, "refusing non-loopback address %q\n", *addr)
		return 2
	}
	if err := os.MkdirAll(filepath.Dir(*db), 0o700); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer s.Close()
	w := &presence.Watcher{Idle: presence.System, Threshold: 2 * time.Minute, Interval: 15 * time.Second, Now: time.Now,
		Emit: func(e event.Event) { s.Append(e) }}
	go w.Run(context.Background())
	srv := &http.Server{Addr: *addr, Handler: (&api.Server{Store: s, Now: time.Now}).Handler(), ReadHeaderTimeout: 5 * time.Second}
	fmt.Fprintf(stderr, "vibetime listening on %s (db %s)\n", *addr, *db)
	fmt.Fprintln(stderr, srv.ListenAndServe())
	return 1
}

// hook reads a harness payload (stdin, or the last argument for Codex notify) and forwards
// it. It always returns 0 so it can never break the harness.
func hook(args []string, stdin io.Reader, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "vibetime hook: harness name required")
		return 0
	}
	var raw []byte
	if len(args) > 1 {
		raw = []byte(args[len(args)-1])
	} else {
		raw, _ = io.ReadAll(io.LimitReader(stdin, 4<<20))
	}
	evs, err := hooks.Parse(args[0], raw, time.Now())
	if err != nil {
		fmt.Fprintln(stderr, "vibetime hook:", err)
		return 0
	}
	os.MkdirAll(dataDir(), 0o700)
	if err := client.New(agentURL(), filepath.Join(dataDir(), "spool.jsonl")).Send(evs); err != nil {
		fmt.Fprintln(stderr, "vibetime hook:", err)
	}
	return 0
}
