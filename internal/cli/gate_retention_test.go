package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/control"
	"github.com/IzieStratt/vibetime/internal/event"
)

func TestPresenceGate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VIBETIME_HOME", dir)
	ctl := &control.Store{Dir: dir}
	now := time.Now()
	open := filepath.Join(t.TempDir(), "open")
	secret := filepath.Join(t.TempDir(), "secret")
	folder, _ := control.Normalize(secret)

	// A stale change time (over a minute old) and an excluded latest folder: withheld.
	ctl.Save(control.Settings{Excluded: []string{folder}, ChangedAt: now.Add(-2 * time.Minute)})
	ctl.NoteCwd(secret)
	if presenceAllowed(ctl, now) {
		t.Fatal("presence recorded while the latest folder is excluded")
	}
	ctl.NoteCwd(open)
	if !presenceAllowed(ctl, now) {
		t.Fatal("presence withheld in an open folder")
	}

	// A change to exclusions or the pause within the last minute withholds presence briefly.
	ctl.Save(control.Settings{ChangedAt: now.Add(-30 * time.Second)})
	if presenceAllowed(ctl, now) {
		t.Fatal("presence recorded within a minute of an exclude/pause change")
	}
	ctl.Save(control.Settings{ChangedAt: now.Add(-2 * time.Minute)})
	if !presenceAllowed(ctl, now) {
		t.Fatal("presence withheld long after a change")
	}

	ctl.Save(control.Settings{PausedUntil: now.Add(time.Hour)})
	if presenceAllowed(ctl, now) {
		t.Fatal("presence recorded while paused")
	}

	// An unreadable control file fails closed.
	os.WriteFile(filepath.Join(dir, "control.json"), []byte("{not json"), 0o600)
	if presenceAllowed(ctl, now) {
		t.Fatal("presence recorded with an unreadable control file")
	}
}

func TestRetentionCommand(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VIBETIME_HOME", dir)
	var out, errw bytes.Buffer
	Stdout = &out
	defer func() { Stdout = os.Stdout }()
	if code := retention(nil, &errw); code != 0 || !bytes.Contains(out.Bytes(), []byte("14 days")) {
		t.Fatalf("default retention output: %d %q", code, out.String())
	}
	if code := retention([]string{"0"}, &errw); code != 0 {
		t.Fatalf("set 0: %d %s", code, errw.String())
	}
	cfg, _ := (&control.Store{Dir: dir}).Load()
	if cfg.Retention() != 0 {
		t.Fatalf("retention = %d, want 0", cfg.Retention())
	}
	for _, bad := range [][]string{{"-1"}, {"soon"}, {"1", "2"}} {
		if code := retention(bad, &errw); code != 2 {
			t.Fatalf("retention %v: exit %d, want 2", bad, code)
		}
	}
}

func TestSubmitWarnsAboutScrubbedPrompts(t *testing.T) {
	evs := []event.Event{
		{Kind: event.Prompt, Project: "p", PromptText: "kept text", PromptLen: 9},
		{Kind: event.Prompt, Project: "p", PromptLen: 40, PromptSHA: "x"}, // scrubbed
		{Kind: event.Prompt, Project: "other", PromptLen: 40},             // other project
		{Kind: event.Prompt, Project: "p"},                                // never had text
		{Kind: event.Edit, Project: "p", PromptLen: 5},
	}
	if n := lostPrompts(evs, "p"); n != 1 {
		t.Fatalf("lostPrompts = %d, want 1", n)
	}
}
