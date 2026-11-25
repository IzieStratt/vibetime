package control

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRetentionDefaultsAndSetting(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	cfg, _ := s.Load()
	if cfg.Retention() != DefaultRetentionDays || DefaultRetentionDays != 14 {
		t.Fatalf("default retention = %d", cfg.Retention())
	}
	if err := s.SetRetention(0); err != nil {
		t.Fatal(err)
	}
	cfg, _ = s.Load()
	if cfg.Retention() != 0 {
		t.Fatalf("0 must mean keep forever, got %d", cfg.Retention())
	}
	if err := s.SetRetention(-1); err == nil {
		t.Fatal("negative retention accepted")
	}
	if err := s.SetRetention(30); err != nil {
		t.Fatal(err)
	}
	if cfg, _ = s.Load(); cfg.Retention() != 30 {
		t.Fatalf("retention = %d, want 30", cfg.Retention())
	}
}

func TestChangeWindowAfterExcludeAndPause(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	cfg, _ := s.Load()
	if cfg.ChangedWithin(time.Now(), time.Minute) {
		t.Fatal("a fresh store has not changed")
	}
	if _, err := s.Exclude(filepath.Join(dir, "x")); err != nil {
		t.Fatal(err)
	}
	cfg, _ = s.Load()
	if !cfg.ChangedWithin(time.Now(), time.Minute) {
		t.Fatal("exclude should count as a recent change")
	}
	if cfg.ChangedWithin(time.Now().Add(2*time.Minute), time.Minute) {
		t.Fatal("the change window must end after a minute")
	}
}

func TestForwardingDoesNotCountAsChange(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.SetForward("https://example.com/hb", "k"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := s.Load()
	if cfg.ChangedWithin(time.Now(), time.Minute) {
		t.Fatal("forwarding settings must not withhold presence")
	}
}
