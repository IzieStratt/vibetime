package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/chain"
	"github.com/IzieStratt/vibetime/internal/event"
)

func open(t *testing.T) (*Store, string) {
	p := filepath.Join(t.TempDir(), "s.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}

func TestOpenCreatesMissingDir(t *testing.T) {
	// A fresh install runs `vibetime status` (which opens the db) before `serve` ever
	// makes the config dir; Open must create it rather than fail with SQLite error 14.
	p := filepath.Join(t.TempDir(), "does", "not", "exist", "events.db")
	s, err := Open(p)
	if err != nil {
		t.Fatalf("open in missing dir: %v", err)
	}
	s.Close()
}

func TestAppendVerifyReopen(t *testing.T) {
	s, p := open(t)
	t0 := time.Unix(1_800_000_000, 0)
	for i := 0; i < 5; i++ {
		if _, err := s.Append(event.Event{Time: t0.Add(time.Duration(i) * time.Minute), Kind: event.Heartbeat, Project: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	links, _ := s.Links()
	if i, err := chain.Verify(chain.Genesis, links); i != -1 {
		t.Fatalf("chain invalid: %v", err)
	}
	head := s.Head()
	s.Close()
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.Head() != head {
		t.Fatal("head not restored on reopen")
	}
	evs, _ := s2.Range(t0, t0.Add(3*time.Minute))
	if len(evs) != 3 {
		t.Fatalf("range got %d", len(evs))
	}
}

func TestRejectsInvalid(t *testing.T) {
	s, _ := open(t)
	defer s.Close()
	if _, err := s.Append(event.Event{Kind: event.Prompt}); err == nil {
		t.Fatal("expected error")
	}
	if s.Head() != chain.Genesis {
		t.Fatal("head moved on invalid event")
	}
}

func TestDBTamperDetected(t *testing.T) {
	s, _ := open(t)
	defer s.Close()
	for i := 0; i < 3; i++ {
		s.Append(event.Event{Time: time.Unix(int64(i*60), 0), Kind: event.Heartbeat, Project: "p"})
	}
	s.DB().Exec(`UPDATE events SET body = replace(body, '"p"', '"q"') WHERE id = 2`)
	links, _ := s.Links()
	if i, _ := chain.Verify(chain.Genesis, links); i != 1 {
		t.Fatalf("expected tamper at 1, got %d", i)
	}
}

func TestCountAndLinksFrom(t *testing.T) {
	s, _ := open(t)
	defer s.Close()
	for i := 0; i < 5; i++ {
		s.Append(event.Event{Time: time.Unix(int64(i*60), 0), Kind: event.Heartbeat, Project: "p"})
	}
	if n, _ := s.Count(); n != 5 {
		t.Fatal(n)
	}
	all, _ := s.Links()
	prev, links, _ := s.LinksFrom(time.Unix(120, 0))
	if prev != all[1].Hash || len(links) != 3 {
		t.Fatal(len(links))
	}
	if i, _ := chain.Verify(prev, links); i != -1 {
		t.Fatal("slice must verify from prev")
	}
	if prev, links, _ := s.LinksFrom(time.Unix(0, 0)); prev != chain.Genesis || len(links) != 5 {
		t.Fatal("from start")
	}
	if _, links, _ := s.LinksFrom(time.Unix(9999, 0)); links != nil {
		t.Fatal("after end")
	}
}
