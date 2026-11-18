package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/event"
)

func ev(p string) event.Event { return event.Event{Time: time.Unix(100, 0).UTC(), Kind: event.Prompt, Project: p} }

func TestSpoolThenFlush(t *testing.T) {
	var got atomic.Int32
	var up atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			w.WriteHeader(503)
			return
		}
		var evs []event.Event
		json.NewDecoder(r.Body).Decode(&evs)
		got.Add(int32(len(evs)))
		w.WriteHeader(202)
	}))
	defer ts.Close()
	spool := filepath.Join(t.TempDir(), "spool.jsonl")
	c := New(ts.URL, spool)

	c.Send([]event.Event{ev("a")})
	c.Send([]event.Event{ev("b")})
	if len(c.readSpool()) != 2 {
		t.Fatal("expected 2 spooled")
	}
	up.Store(true)
	if err := c.Send([]event.Event{ev("c")}); err != nil {
		t.Fatal(err)
	}
	if got.Load() != 3 {
		t.Fatalf("delivered %d", got.Load())
	}
	if _, err := os.Stat(spool); !os.IsNotExist(err) {
		t.Fatal("spool not cleared")
	}
}

func TestAgentDownDoesNotBlock(t *testing.T) {
	c := New("http://127.0.0.1:1", filepath.Join(t.TempDir(), "s"))
	start := time.Now()
	if err := c.Send([]event.Event{ev("a")}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("blocked too long")
	}
}

func TestBadRequestDropped(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
	defer ts.Close()
	spool := filepath.Join(t.TempDir(), "s")
	New(ts.URL, spool).Send([]event.Event{ev("a")})
	if _, err := os.Stat(spool); !os.IsNotExist(err) {
		t.Fatal("invalid events should not be spooled forever")
	}
}

func TestEmptyNoop(t *testing.T) {
	if err := New("http://127.0.0.1:1", filepath.Join(t.TempDir(), "s")).Send(nil); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptSpoolLinesSkipped(t *testing.T) {
	spool := filepath.Join(t.TempDir(), "s")
	os.WriteFile(spool, []byte("garbage\n{\"time\":\"1970-01-01T00:01:40Z\",\"kind\":\"prompt\",\"project\":\"a\"}\n"), 0o600)
	if n := len(New("", spool).readSpool()); n != 1 {
		t.Fatalf("got %d", n)
	}
}

func TestWriteSpoolError(t *testing.T) {
	if err := New("", filepath.Join(t.TempDir(), "missing", "s")).writeSpool([]event.Event{ev("a")}); err == nil {
		t.Fatal("expected error")
	}
}
