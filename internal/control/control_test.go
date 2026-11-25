package control

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExcludeMatchesFolderAndChildrenOnly(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	secret := filepath.Join(dir, "secret")
	os.MkdirAll(filepath.Join(secret, "sub"), 0o700)
	if _, err := s.Exclude(secret); err != nil {
		t.Fatal(err)
	}
	cfg, _ := s.Load()
	cases := map[string]bool{
		secret:                              true,
		filepath.Join(secret, "sub"):        true,
		filepath.Join(secret, "sub", "x"):   true,
		dir:                                 false,
		secret + "-other":                   false, // a sibling sharing a name prefix is not inside
		filepath.Join(dir, "secrets-extra"): false,
		"":                                  false,
	}
	for cwd, want := range cases {
		if got := cfg.Excludes(cwd); got != want {
			t.Errorf("Excludes(%q) = %v, want %v", cwd, got, want)
		}
	}
}

func TestExcludeListAndRemove(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	s.Exclude(a)
	s.Exclude(a) // idempotent
	s.Exclude(b)
	cfg, _ := s.Load()
	if len(cfg.Excluded) != 2 {
		t.Fatalf("want 2 excluded folders, got %v", cfg.Excluded)
	}
	removed, err := s.Include(a)
	if err != nil || !removed {
		t.Fatalf("Include(a) = %v, %v", removed, err)
	}
	if removed, _ := s.Include(a); removed {
		t.Error("removing twice should report nothing removed")
	}
	cfg, _ = s.Load()
	if len(cfg.Excluded) != 1 || cfg.Excludes(a) || !cfg.Excludes(b) {
		t.Errorf("after remove: %v", cfg.Excluded)
	}
}

func TestExcludeRejectsEmpty(t *testing.T) {
	if _, err := (&Store{Dir: t.TempDir()}).Exclude("  "); err == nil {
		t.Error("empty folder should be rejected")
	}
}

func TestPauseAndResume(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.Local)
	until, err := ParsePause("30m", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Pause(until); err != nil {
		t.Fatal(err)
	}
	cfg, _ := s.Load()
	if !cfg.Paused(now.Add(29*time.Minute)) || cfg.Paused(now.Add(31*time.Minute)) {
		t.Errorf("pause window wrong: until %v", cfg.PausedUntil)
	}
	if err := s.Resume(); err != nil {
		t.Fatal(err)
	}
	cfg, _ = s.Load()
	if cfg.Paused(now) {
		t.Error("resume should clear the pause")
	}
}

func TestParsePause(t *testing.T) {
	now := time.Date(2026, 11, 24, 22, 30, 0, 0, time.Local)
	cases := map[string]time.Time{
		"30m":      now.Add(30 * time.Minute),
		"1h":       now.Add(time.Hour),
		"2h":       now.Add(2 * time.Hour),
		"tomorrow": time.Date(2026, 11, 25, 0, 0, 0, 0, time.Local),
	}
	for arg, want := range cases {
		got, err := ParsePause(arg, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("ParsePause(%q) = %v, %v; want %v", arg, got, err, want)
		}
	}
	for _, bad := range []string{"", "soon", "-1h", "0s"} {
		if _, err := ParsePause(bad, now); err == nil {
			t.Errorf("ParsePause(%q) should fail", bad)
		}
	}
}

func TestSetForwardValidatesURL(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.SetForward("ftp://x", ""); err == nil {
		t.Error("non-http URL should be rejected")
	}
	if err := s.SetForward("https://hackatime.example/api", "k"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := s.Load()
	if cfg.ForwardURL == "" || cfg.ForwardKey != "k" {
		t.Errorf("forward settings not saved: %+v", cfg)
	}
	if err := s.SetForward("", ""); err != nil {
		t.Fatal(err)
	}
	cfg, _ = s.Load()
	if cfg.ForwardURL != "" {
		t.Error("empty url should turn forwarding off")
	}
}

func TestDefaultIsOpen(t *testing.T) {
	cfg, err := (&Store{Dir: t.TempDir()}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Paused(time.Now()) || cfg.ForwardURL != "" || cfg.Excludes("/anything") {
		t.Errorf("zero config should change nothing: %+v", cfg)
	}
}

func TestLastCwd(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	s.NoteCwd("/a")
	s.NoteCwd("") // empty never overwrites
	if s.LastCwd() != "/a" {
		t.Errorf("LastCwd = %q", s.LastCwd())
	}
}
