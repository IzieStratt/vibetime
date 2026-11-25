package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/chain"
	"github.com/IzieStratt/vibetime/internal/event"
)

// farFuture is a bound that still fits in the int64 nanoseconds the store compares against.
var farFuture = time.Unix(4_000_000_000, 0)

const secret = "my client's private API key is abc123 please fix"

func promptAt(i int, text string) event.Event {
	return event.Event{Time: time.Unix(1_800_000_000+int64(i)*60, 0), Kind: event.Prompt, Project: "p", PromptText: text}
}

func verifyChain(t *testing.T, s *Store) {
	t.Helper()
	links, err := s.Links()
	if err != nil {
		t.Fatal(err)
	}
	if i, err := chain.Verify(chain.Genesis, links); i != -1 {
		t.Fatalf("chain invalid at %d: %v", i, err)
	}
}

func TestPromptTextIsNotPlaintextOnDisk(t *testing.T) {
	s, _ := open(t)
	defer s.Close()
	s.Append(promptAt(0, secret))
	var body string
	var enc []byte
	if err := s.DB().QueryRow(`SELECT body, prompt_enc FROM events`).Scan(&body, &enc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "abc123") || strings.Contains(string(enc), "abc123") {
		t.Fatal("prompt text found in plaintext on disk")
	}
	if !strings.Contains(body, "prompt_sha256") {
		t.Fatal("body should keep the committed hash")
	}
	evs, err := s.Range(time.Unix(0, 0), farFuture)
	if err != nil || len(evs) != 1 || evs[0].PromptText != secret {
		t.Fatalf("round trip failed: %v %+v", err, evs)
	}
	verifyChain(t, s)
}

func TestKeyFileIsOwnerOnlyAndReused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	s.Append(promptAt(0, secret))
	s.Close()
	info, err := os.Stat(filepath.Join(filepath.Dir(p), KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no unix mode bits (files report 0666); there the user-profile ACL applies.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v, want 0600", info.Mode().Perm())
	}
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	evs, _ := s2.Range(time.Unix(0, 0), farFuture)
	if len(evs) != 1 || evs[0].PromptText != secret {
		t.Fatal("reopen lost the prompt text: key was not reused")
	}
}

func TestScrubKeepsChainValidAndHashes(t *testing.T) {
	s, _ := open(t)
	defer s.Close()
	for i := 0; i < 6; i++ {
		s.Append(promptAt(i, secret))
	}
	headBefore := s.Head()
	before, _ := s.Links()
	cut := time.Unix(1_800_000_000+3*60, 0) // first three prompts are older than the cutoff
	n, err := s.Scrub(cut)
	if err != nil || n != 3 {
		t.Fatalf("scrubbed %d (err %v), want 3", n, err)
	}
	if s.Head() != headBefore {
		t.Fatal("scrub moved the chain head")
	}
	verifyChain(t, s)
	after, _ := s.Links()
	for i := range after {
		if after[i].Hash != before[i].Hash {
			t.Fatalf("hash of event %d changed by scrub", i)
		}
		if i < 3 && after[i].Event.PromptText != "" {
			t.Fatalf("event %d still has text", i)
		}
		if i >= 3 && after[i].Event.PromptText != secret {
			t.Fatalf("recent event %d lost its text", i)
		}
		if after[i].Event.PromptSHA != before[i].Event.PromptSHA || after[i].Event.PromptLength() != before[i].Event.PromptLength() {
			t.Fatalf("event %d lost its committed hash or length", i)
		}
	}
	// Idempotent: a second scrub finds nothing to do and the chain still holds.
	if n, err := s.Scrub(cut); err != nil || n != 0 {
		t.Fatalf("second scrub: %d %v", n, err)
	}
	verifyChain(t, s)
}

func TestScrubLegacyPlaintextRows(t *testing.T) {
	// Rows written before encryption kept the text inside the body. Scrub must clear it too,
	// and the chain must still verify because the hashes are recomputed from the same text.
	s, _ := open(t)
	defer s.Close()
	prev := chain.Genesis
	for i := 0; i < 3; i++ {
		e := promptAt(i, secret+" "+strings.Repeat("x", i))
		h, _ := chain.Next(prev, e)
		b, _ := json.Marshal(e)
		if _, err := s.DB().Exec(`INSERT INTO events(ts, body, hash) VALUES(?,?,?)`, e.Time.UnixNano(), string(b), h); err != nil {
			t.Fatal(err)
		}
		prev = h
	}
	s.head = prev
	verifyChain(t, s)
	if n, err := s.Scrub(farFuture); err != nil || n != 3 {
		t.Fatalf("scrubbed %d %v", n, err)
	}
	var body string
	s.DB().QueryRow(`SELECT body FROM events WHERE id = 1`).Scan(&body)
	if strings.Contains(body, "private API") {
		t.Fatal("legacy plaintext survived scrub")
	}
	verifyChain(t, s)
}

func TestScrubRefusesTamperedRow(t *testing.T) {
	s, _ := open(t)
	defer s.Close()
	s.Append(promptAt(0, secret))
	s.Append(promptAt(1, secret))
	s.DB().Exec(`UPDATE events SET body = replace(body, '"p"', '"q"') WHERE id = 1`)
	var before string
	s.DB().QueryRow(`SELECT body FROM events WHERE id = 1`).Scan(&before)
	if _, err := s.Scrub(farFuture); err == nil {
		t.Fatal("scrub must refuse a row whose hash no longer matches")
	}
	var after string
	s.DB().QueryRow(`SELECT body FROM events WHERE id = 1`).Scan(&after)
	if before != after {
		t.Fatal("tampered row was rewritten")
	}
}

func TestCiphertextBoundToItsEvent(t *testing.T) {
	// Moving one event's encrypted text onto another event must not decrypt: the chain hash is
	// authenticated data. Verification still passes because it never needs the text.
	s, _ := open(t)
	defer s.Close()
	s.Append(promptAt(0, "first prompt text, long enough"))
	s.Append(promptAt(1, "second prompt text, also long"))
	var a []byte
	s.DB().QueryRow(`SELECT prompt_enc FROM events WHERE id = 1`).Scan(&a)
	s.DB().Exec(`UPDATE events SET prompt_enc = ? WHERE id = 2`, a)
	evs, _ := s.Range(time.Unix(0, 0), farFuture)
	if evs[1].PromptText != "" {
		t.Fatal("ciphertext moved between events decrypted")
	}
	verifyChain(t, s)
}
