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
	"strconv"
	"strings"
	"time"

	"github.com/IzieStratt/vibetime/internal/api"
	"github.com/IzieStratt/vibetime/internal/client"
	"github.com/IzieStratt/vibetime/internal/control"
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
  exclude <folder>   never record activity inside this folder (-list, -remove <folder>)
  pause [DURATION]   stop recording for 30m, 1h, 2h or tomorrow (default 30m); resume ends it early
  resume             end a pause
  retention [DAYS]   keep prompt text for DAYS days (default 14; 0 keeps it forever)
  forward            -url URL [-key KEY] | -off   also send WakaTime heartbeats to another endpoint
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
	case "exclude":
		return exclude(args[1:], stderr)
	case "pause":
		return pause(args[1:], stderr)
	case "resume":
		return resume(stderr)
	case "retention":
		return retention(args[1:], stderr)
	case "forward":
		return forward(args[1:], stderr)
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
	ctl := &control.Store{Dir: dataDir()}
	// Presence carries no project, so it is gated (see presenceAllowed) before it is stored.
	w := &presence.Watcher{Idle: presence.System, Threshold: 2 * time.Minute, Interval: 15 * time.Second, Now: time.Now,
		Emit: func(e event.Event) {
			if presenceAllowed(ctl, time.Now()) {
				s.Append(e)
			}
		}}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go w.Run(ctx)
	go scrubLoop(ctx, s, ctl, stderr)
	if cfg, err := syncer.LoadConfig(dataDir()); err == nil && cfg.Server != "" {
		sy := &syncer.Syncer{Store: s, Cfg: cfg, Now: time.Now}
		go sy.Run(time.Minute, ctx.Done(), func(err error) { fmt.Fprintln(stderr, "sync:", err) })
		fmt.Fprintf(stderr, "syncing to %s as device %s\n", cfg.Server, cfg.Device)
	}
	srv := &http.Server{Addr: *addr, Handler: (&api.Server{Store: s, Now: time.Now, Control: ctl}).Handler(), ReadHeaderTimeout: 5 * time.Second}
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

// changeHold is how long presence is withheld after an exclude or pause change.
const changeHold = time.Minute

// presenceAllowed reports whether a presence event may be stored now. Presence carries no folder,
// so it is withheld while paused, while the latest event's folder is excluded, and for a minute
// after an exclude or pause change (the latest folder may be stale then). An unreadable control
// file also withholds it: presence is evidence, so when in doubt it is not recorded.
func presenceAllowed(ctl *control.Store, now time.Time) bool {
	cfg, err := ctl.Load()
	if err != nil {
		return false
	}
	return !cfg.Paused(now) && !cfg.Excludes(ctl.LastCwd()) && !cfg.ChangedWithin(now, changeHold)
}

// scrubLoop removes prompt text older than the configured retention at start and then hourly.
// The chain keeps every hash and length, so verification is unaffected.
func scrubLoop(ctx context.Context, s *store.Store, ctl *control.Store, stderr io.Writer) {
	run := func() {
		cfg, err := ctl.Load()
		if err != nil {
			fmt.Fprintln(stderr, "retention:", err)
			return
		}
		days := cfg.Retention()
		if days == 0 {
			return
		}
		n, err := s.Scrub(time.Now().AddDate(0, 0, -days))
		if err != nil {
			fmt.Fprintln(stderr, "retention:", err)
		}
		if n > 0 {
			fmt.Fprintf(stderr, "retention: removed prompt text from %d events older than %d days\n", n, days)
		}
	}
	run()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
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
	if *prompts {
		evs, err := s.Range(f, t)
		if err != nil {
			fmt.Fprintln(stderr, "submit:", err)
			return 1
		}
		if lost := lostPrompts(evs, *project); lost > 0 {
			fmt.Fprintf(stderr, "submit: warning: %d prompts in this window have no stored text (removed by retention, or never kept); those are uploaded as hashes and lengths only\n", lost)
		}
	}
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

// lostPrompts counts the project's prompts that were recorded with text but have none now
// (scrubbed by retention). Such prompts can only be uploaded as hashes.
func lostPrompts(evs []event.Event, project string) int {
	n := 0
	for _, e := range evs {
		if e.Kind == event.Prompt && e.Project == project && e.PromptText == "" && e.PromptLen > 0 {
			n++
		}
	}
	return n
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
	if cfg, err := (&control.Store{Dir: dataDir()}).Load(); err == nil {
		if cfg.Paused(now) {
			fmt.Fprintf(Stdout, "recording paused until %s (vibetime resume to end early)\n", cfg.PausedUntil.Local().Format("Mon 15:04"))
		}
		if len(cfg.Excluded) > 0 {
			fmt.Fprintf(Stdout, "excluded folders: %d (vibetime exclude -list)\n", len(cfg.Excluded))
		}
	}
	return 0
}

func exclude(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("exclude", flag.ContinueOnError)
	fs.SetOutput(stderr)
	list := fs.Bool("list", false, "list excluded folders")
	remove := fs.String("remove", "", "stop excluding this folder")
	ctl := &control.Store{Dir: dataDir()}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *list {
		cfg, err := ctl.Load()
		if err != nil {
			fmt.Fprintln(stderr, "exclude:", err)
			return 1
		}
		for _, f := range cfg.Excluded {
			fmt.Fprintln(Stdout, f)
		}
		return 0
	}
	if *remove != "" {
		removed, err := ctl.Include(*remove)
		if err != nil {
			fmt.Fprintln(stderr, "exclude:", err)
			return 1
		}
		if !removed {
			fmt.Fprintln(stderr, "exclude: that folder was not excluded")
			return 1
		}
		fmt.Fprintln(stderr, "no longer excluded")
		return 0
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: vibetime exclude <folder> | -list | -remove <folder>")
		return 2
	}
	p, err := ctl.Exclude(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, "exclude:", err)
		return 1
	}
	fmt.Fprintf(stderr, "excluded %s: nothing inside it is recorded from now on\n", p)
	return 0
}

func retention(args []string, stderr io.Writer) int {
	ctl := &control.Store{Dir: dataDir()}
	if len(args) == 0 {
		cfg, err := ctl.Load()
		if err != nil {
			fmt.Fprintln(stderr, "retention:", err)
			return 1
		}
		if cfg.Retention() == 0 {
			fmt.Fprintln(Stdout, "prompt text is kept forever")
		} else {
			fmt.Fprintf(Stdout, "prompt text is kept for %d days, then removed from the local database (hashes stay)\n", cfg.Retention())
		}
		return 0
	}
	days, err := strconv.Atoi(args[0])
	if len(args) != 1 || err != nil || days < 0 {
		fmt.Fprintln(stderr, "usage: vibetime retention [DAYS]  (0 keeps prompt text forever)")
		return 2
	}
	if err := ctl.SetRetention(days); err != nil {
		fmt.Fprintln(stderr, "retention:", err)
		return 2
	}
	fmt.Fprintln(stderr, "saved; the running agent applies it within the hour. Removed text cannot be restored")
	return 0
}

func pause(args []string, stderr io.Writer) int {
	arg := "30m"
	if len(args) == 1 {
		arg = args[0]
	} else if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: vibetime pause [30m|1h|2h|tomorrow]")
		return 2
	}
	until, err := control.ParsePause(arg, time.Now())
	if err == nil {
		err = (&control.Store{Dir: dataDir()}).Pause(until)
	}
	if err != nil {
		fmt.Fprintln(stderr, "pause:", err)
		return 2
	}
	fmt.Fprintf(stderr, "paused until %s; vibetime resume ends it early\n", until.Local().Format("Mon 15:04"))
	return 0
}

func resume(stderr io.Writer) int {
	if err := (&control.Store{Dir: dataDir()}).Resume(); err != nil {
		fmt.Fprintln(stderr, "resume:", err)
		return 1
	}
	fmt.Fprintln(stderr, "resumed")
	return 0
}

func forward(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("forward", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", "", "endpoint to receive a copy of each WakaTime heartbeat (e.g. your Hackatime API)")
	key := fs.String("key", "", "API key sent as HTTP Basic auth (WakaTime style)")
	off := fs.Bool("off", false, "stop forwarding")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctl := &control.Store{Dir: dataDir()}
	var err error
	switch {
	case *off:
		err = ctl.SetForward("", "")
	case *url != "":
		err = ctl.SetForward(*url, *key)
	default:
		cfg, lerr := ctl.Load()
		if lerr != nil {
			fmt.Fprintln(stderr, "forward:", lerr)
			return 1
		}
		if cfg.ForwardURL == "" {
			fmt.Fprintln(stderr, "forwarding is off; usage: vibetime forward -url URL [-key KEY] | -off")
		} else {
			fmt.Fprintf(stderr, "forwarding heartbeats to %s\n", cfg.ForwardURL)
		}
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "forward:", err)
		return 2
	}
	fmt.Fprintln(stderr, "forward settings saved (the running agent picks them up on the next heartbeat)")
	return 0
}
