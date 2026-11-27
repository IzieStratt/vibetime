package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/control"
)

func cfgNow() time.Time { return time.Now() }

// The control commands edit the same control.json the agent reads.
func TestExcludePauseForwardCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIBETIME_HOME", home)
	folder := t.TempDir()
	var out, errb bytes.Buffer
	oldOut := Stdout
	Stdout = &out
	defer func() { Stdout = oldOut }()

	if code := RunContext(t.Context(), []string{"exclude", folder}, nil, &errb); code != 0 {
		t.Fatalf("exclude: %d %s", code, errb.String())
	}
	out.Reset()
	if code := RunContext(t.Context(), []string{"exclude", "-list"}, nil, &errb); code != 0 {
		t.Fatalf("list: %d", code)
	}
	if !strings.Contains(out.String(), filepath.Base(folder)) {
		t.Errorf("list should show the folder, got %q", out.String())
	}
	if code := RunContext(t.Context(), []string{"exclude", "-remove", folder}, nil, &errb); code != 0 {
		t.Fatalf("remove: %d %s", code, errb.String())
	}
	if code := RunContext(t.Context(), []string{"exclude", "-remove", folder}, nil, &errb); code == 0 {
		t.Error("removing a folder that is not excluded should fail")
	}

	if code := RunContext(t.Context(), []string{"pause", "1h"}, nil, &errb); code != 0 {
		t.Fatalf("pause: %d %s", code, errb.String())
	}
	cfg, _ := (&control.Store{Dir: home}).Load()
	if !cfg.Paused(cfgNow()) {
		t.Error("pause 1h should be in effect")
	}
	if code := RunContext(t.Context(), []string{"pause", "tomorrow"}, nil, &errb); code != 0 {
		t.Fatalf("pause tomorrow: %d", code)
	}
	if code := RunContext(t.Context(), []string{"pause", "soon"}, nil, &errb); code != 2 {
		t.Errorf("bad pause duration should be a usage error, got %d", code)
	}
	if code := RunContext(t.Context(), []string{"resume"}, nil, &errb); code != 0 {
		t.Fatalf("resume: %d", code)
	}
	cfg, _ = (&control.Store{Dir: home}).Load()
	if cfg.Paused(cfgNow()) {
		t.Error("resume should end the pause")
	}

	if code := RunContext(t.Context(), []string{"forward", "-url", "ftp://nope"}, nil, &errb); code == 0 {
		t.Error("non-http forward URL should be rejected")
	}
	if code := RunContext(t.Context(), []string{"forward", "-url", "https://hackatime.example/api", "-key", "k"}, nil, &errb); code != 0 {
		t.Fatalf("forward: %d %s", code, errb.String())
	}
	cfg, _ = (&control.Store{Dir: home}).Load()
	if cfg.ForwardURL != "https://hackatime.example/api" || cfg.ForwardKey != "k" {
		t.Errorf("forward not saved: %+v", cfg)
	}
	if code := RunContext(t.Context(), []string{"forward", "-off"}, nil, &errb); code != 0 {
		t.Fatalf("forward off: %d", code)
	}
	cfg, _ = (&control.Store{Dir: home}).Load()
	if cfg.ForwardURL != "" {
		t.Error("forward -off should clear the URL")
	}
}
