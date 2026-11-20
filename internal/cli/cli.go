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
	"github.com/IzieStratt/vibetime/internal/report"
	"github.com/IzieStratt/vibetime/internal/service"
	"github.com/IzieStratt/vibetime/internal/store"
	"github.com/IzieStratt/vibetime/internal/syncer"
)

// Stdout receives hook replies for harnesses that read one (swapped in tests).
var Stdout io.Writer = os.Stdout

const DefaultAddr = "127.0.0.1:47391"

const usage = `vibetime - count attended (human-in-the-loop) AI coding time

usage: vibetime <command> [flags]

commands:
  serve              run the local agent (loopback HTTP + presence watcher); default
  hook <harness>     forward a harness hook payload (claude-code, codex, gemini-cli, ...)
  status [-days N]   show today/total attended time, split hands-on/steering/watching
  login              -server URL (-handle NAME | -token TOKEN)
  submit             -project NAME -repo REPO -from RFC3339 [-to RFC3339] [-include-prompts]
  import-hackatime   -key KEY [-since YYYY-MM-DD]
  service            install | uninstall  (start vibetime at login)
  version            print the version

run "vibetime <command> -h" for a command's flags.
`

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
	return RunContext(context.Background(), args, stdin, stderr)
}

// RunContext is Run with cancellation: cancelling ctx shuts `serve` down gracefully.
func RunContext(ctx context.Context, args []string, stdin io.Reader, stderr io.Writer) int {
	if len(args) == 0 {
		args = []string{"serve"}
	}
	switch args[0] {
	case "serve":
		return serve(ctx, args[1:], stderr)
	case "hook":
		return hook(args[1:], stdin, stderr)
	case "status":
		return status(args[1:], stderr)
	case "login":
		return login(args[1:], stderr)
	case "submit":
		return submit(args[1:], stderr)
	case "import-hackatime":
		return importHackatime(args[1:], stderr)
	case "service":
		return svc(args[1:], stderr)
	case "version":
		fmt.Fprintln(stderr, "vibetime 0.1.0")
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stderr, usage)
		return 0
	}
	fmt.Fprint(stderr, usage)
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

func serve(ctx context.Context, args []string, stderr io.Writer) int {
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go w.Run(ctx)
	if cfg, err := syncer.LoadConfig(dataDir()); err == nil && cfg.Server != "" {
		sy := &syncer.Syncer{Store: s, Cfg: cfg, Now: time.Now}
		go sy.Run(time.Minute, ctx.Done(), func(err error) { fmt.Fprintln(stderr, "sync:", err) })
		fmt.Fprintf(stderr, "syncing to %s as device %s\n", cfg.Server, cfg.Device)
	}
	srv := &http.Server{Addr: *addr, Handler: (&api.Server{Store: s, Now: time.Now}).Handler(), ReadHeaderTimeout: 5 * time.Second}
	fmt.Fprintf(stderr, "vibetime listening on %s (db %s)\n", *addr, *db)
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		fmt.Fprintln(stderr, err)
		return 1
	case <-ctx.Done():
		sc, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		srv.Shutdown(sc)
		return 0
	}
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
	if r := hooks.Reply(args[0], raw); r != "" {
		fmt.Fprintln(Stdout, r) // answer first: the harness may wait for it
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

func login(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	srv := fs.String("server", "", "server URL")
	handle := fs.String("handle", "", "new account handle (open-signup servers)")
	token := fs.String("token", "", "existing API token (from the website)")
	if err := fs.Parse(args); err != nil || *srv == "" || (*handle == "") == (*token == "") {
		fmt.Fprintln(stderr, "usage: vibetime login -server URL (-handle NAME | -token TOKEN)")
		return 2
	}
	var cfg syncer.Config
	var err error
	var who string
	if *token != "" {
		// Verify the token against the server before saving, so a bad token fails here
		// rather than silently storing and only erroring later at sync (401)/submit.
		if who, err = syncer.Verify(*srv, *token, nil); err == nil {
			err = syncer.SaveConfig(dataDir(), syncer.Config{Server: *srv, Token: *token})
		}
		if err == nil {
			cfg, err = syncer.LoadConfig(dataDir())
		}
	} else {
		cfg, err = syncer.Login(dataDir(), *srv, *handle, nil)
		who = *handle
	}
	if err != nil {
		fmt.Fprintln(stderr, "login:", err)
		return 1
	}
	as := ""
	if who != "" {
		as = " as " + who
	}
	fmt.Fprintf(stderr, "logged in to %s%s (device %s); restart `vibetime serve` to start syncing\n", cfg.Server, as, cfg.Device)
	return 0
}

func submit(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "project name")
	repo := fs.String("repo", "", "repository, e.g. github.com/you/project")
	from := fs.String("from", "", "start (RFC3339)")
	to := fs.String("to", "", "end (RFC3339, default now)")
	prompts := fs.Bool("include-prompts", false, "upload this project's prompt text (otherwise only hashes and lengths)")
	db := fs.String("db", filepath.Join(dataDir(), "events.db"), "SQLite path")
	if err := fs.Parse(args); err != nil || *project == "" || *from == "" {
		fmt.Fprintln(stderr, "usage: vibetime submit -project NAME -repo REPO -from RFC3339 [-to RFC3339] [-include-prompts]")
		return 2
	}
	f, err1 := time.Parse(time.RFC3339, *from)
	t := time.Now()
	var err2 error
	if *to != "" {
		t, err2 = time.Parse(time.RFC3339, *to)
	}
	if err1 != nil || err2 != nil {
		fmt.Fprintln(stderr, "submit: times must be RFC3339, e.g. 2026-11-18T15:00:00Z")
		return 2
	}
	cfg, err := syncer.LoadConfig(dataDir())
	if err != nil {
		fmt.Fprintln(stderr, "submit: not logged in (run vibetime login)")
		return 1
	}
	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer s.Close()
	sub, err := (&syncer.Syncer{Store: s, Cfg: cfg, Now: time.Now}).Submit(*project, hooks.NormalizeRepo(*repo), f, t, *prompts)
	if err != nil {
		fmt.Fprintln(stderr, "submit:", err)
		return 1
	}
	fmt.Fprintf(stderr, "submission %d: %s, %.1fh verified, risk %d\n", sub.ID, sub.Status, float64(sub.VerifiedSeconds)/3600, sub.Risk)
	for _, fl := range sub.Result.Flags {
		fmt.Fprintf(stderr, "  [%s] %s\n", fl.Code, fl.Detail)
	}
	return 0
}

// svcInstall/svcUninstall are swapped in tests so they never touch the real machine.
var svcInstall, svcUninstall = service.Install, service.Uninstall

func svc(args []string, stderr io.Writer) int {
	var err error
	switch {
	case len(args) == 1 && args[0] == "install":
		err = svcInstall()
	case len(args) == 1 && args[0] == "uninstall":
		err = svcUninstall()
	default:
		fmt.Fprintln(stderr, "usage: vibetime service install|uninstall")
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "service:", err)
		return 1
	}
	if args[0] == "install" {
		fmt.Fprintln(stderr, "installed: vibetime now starts at login")
	} else {
		fmt.Fprintln(stderr, "uninstalled")
	}
	return 0
}

func importHackatime(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("import-hackatime", flag.ContinueOnError)
	fs.SetOutput(stderr)
	key := fs.String("key", os.Getenv("HACKATIME_API_KEY"), "Hackatime API key (or HACKATIME_API_KEY)")
	since := fs.String("since", "2024-01-01", "first day to import (YYYY-MM-DD)")
	base := fs.String("hackatime-url", syncer.HackatimeURL, "Hackatime API base")
	if err := fs.Parse(args); err != nil || *key == "" {
		fmt.Fprintln(stderr, "usage: vibetime import-hackatime -key KEY [-since 2024-01-01]")
		return 2
	}
	from, err := time.Parse("2006-01-02", *since)
	if err != nil {
		fmt.Fprintln(stderr, "import-hackatime: -since must be YYYY-MM-DD")
		return 2
	}
	cfg, err := syncer.LoadConfig(dataDir())
	if err != nil {
		fmt.Fprintln(stderr, "import-hackatime: not logged in (run vibetime login)")
		return 1
	}
	n, err := (&syncer.Syncer{Cfg: cfg, Now: time.Now}).ImportHackatime(*base, *key, from, time.Now())
	if err != nil {
		fmt.Fprintln(stderr, "import-hackatime:", err)
		return 1
	}
	fmt.Fprintf(stderr, "imported %d project-days from Hackatime (shown as unverified; they never count toward submissions)\n", n)
	return 0
}

func status(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	days := fs.Int("days", 7, "number of days to summarize")
	db := fs.String("db", filepath.Join(dataDir(), "events.db"), "SQLite path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer s.Close()
	now := time.Now()
	evs, err := s.Range(now.AddDate(0, 0, -*days), now.Add(time.Minute))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprint(Stdout, report.Render(evs, now, *days, time.Local))
	return 0
}
