// Package store persists the local event chain in SQLite.
//
// Prompt text is kept out of the chained body. The body stores the redacted event (hashes and
// length only), which is exactly what the chain commits to, and the text itself lives in a
// separate prompt_enc column encrypted with AES-GCM under a 0600 key file next to the database.
// The chain hash is bound in as associated data, so a ciphertext cannot be moved to another row.
// Scrub removes the text of old events without touching anything the chain commits to.
package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/IzieStratt/vibetime/internal/chain"
	"github.com/IzieStratt/vibetime/internal/event"
	_ "modernc.org/sqlite"
)

// KeyFile is the name of the prompt encryption key, stored in the same directory as the database.
const KeyFile = "prompt.key"

type Store struct {
	mu   sync.Mutex
	db   *sql.DB
	head string
	aead cipher.AEAD // seals prompt text at rest
}

func Open(path string) (*Store, error) {
	// Create the parent directory if it is missing: otherwise a fresh install running
	// `vibetime status` (its first command) before `serve` dies with SQLite error 14
	// (unable to open database file). An in-memory DSN has no directory to make.
	memory := strings.HasPrefix(path, ":memory:") || strings.HasPrefix(path, "file::memory:")
	dir := filepath.Dir(path)
	if dir != "" && dir != "." && !memory {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	// WAL + busy_timeout lets a reader (e.g. `vibetime submit` or tests) coexist with the
	// running agent's writer; without WAL, Windows file-locking blocks the second opener.
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts INTEGER NOT NULL,
		body TEXT NOT NULL,
		hash TEXT NOT NULL)`); err != nil {
		return nil, err
	}
	var hasEnc int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('events') WHERE name = 'prompt_enc'`).Scan(&hasEnc); err != nil {
		return nil, err
	}
	if hasEnc == 0 {
		if _, err := db.Exec(`ALTER TABLE events ADD COLUMN prompt_enc BLOB`); err != nil {
			return nil, err
		}
	}
	s := &Store{db: db, head: chain.Genesis}
	key, err := keyFor(dir, memory)
	if err != nil {
		db.Close()
		return nil, err
	}
	if s.aead, err = newAEAD(key); err != nil {
		db.Close()
		return nil, err
	}
	if err := db.QueryRow(`SELECT hash FROM events ORDER BY id DESC LIMIT 1`).Scan(&s.head); err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	return s, nil
}

// keyFor returns the 32-byte prompt key: the file in dir, created with mode 0600 if missing. An
// in-memory database gets a throwaway key.
func keyFor(dir string, memory bool) ([]byte, error) {
	k := make([]byte, 32)
	if memory {
		_, err := rand.Read(k)
		return k, err
	}
	p := filepath.Join(dir, KeyFile)
	b, err := os.ReadFile(p)
	if err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("%s: want a 32-byte key, have %d bytes", p, len(b))
		}
		return b, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return keyFor(dir, memory) // another opener created it first
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Write(k); err != nil {
		return nil, err
	}
	return k, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// seal encrypts text, binding it to the chain hash of its event.
func (s *Store) seal(hash, text string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, []byte(text), []byte(hash)), nil
}

// open decrypts a prompt blob for the event with the given hash.
func (s *Store) open(hash string, blob []byte) (string, error) {
	n := s.aead.NonceSize()
	if len(blob) < n {
		return "", errors.New("prompt blob too short")
	}
	pt, err := s.aead.Open(nil, blob[:n], blob[n:], []byte(hash))
	return string(pt), err
}

// restore puts the decrypted text back on e. A blob that will not decrypt leaves the text empty,
// which is harmless for verification: the chain commits to the stored hashes, not to the text.
func (s *Store) restore(e *event.Event, hash string, blob []byte) {
	if len(blob) == 0 {
		return
	}
	if pt, err := s.open(hash, blob); err == nil {
		e.PromptText = pt
	}
}

func (s *Store) Close() error { return s.db.Close() }

// Append validates, chains and stores an event, returning the new head.
func (s *Store) Append(e event.Event) (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	e.Time = e.Time.UTC()
	e.Cwd = "" // transient: never chained, stored or uploaded
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := chain.Next(s.head, e)
	if err != nil {
		return "", err
	}
	var enc []byte
	if e.PromptText != "" {
		if enc, err = s.seal(h, e.PromptText); err != nil {
			return "", err
		}
	}
	b, _ := json.Marshal(e.Redacted())
	if _, err := s.db.Exec(`INSERT INTO events(ts, body, hash, prompt_enc) VALUES(?,?,?,?)`, e.Time.UnixNano(), string(b), h, enc); err != nil {
		return "", err
	}
	s.head = h
	return h, nil
}

func (s *Store) Head() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.head
}

// Links returns all chain links in insertion order. Prompt text is decrypted when present.
func (s *Store) Links() ([]chain.Link, error) {
	rows, err := s.db.Query(`SELECT body, hash, prompt_enc FROM events ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chain.Link
	for rows.Next() {
		var body, h string
		var enc []byte
		if err := rows.Scan(&body, &h, &enc); err != nil {
			return nil, err
		}
		var e event.Event
		if err := json.Unmarshal([]byte(body), &e); err != nil {
			return nil, err
		}
		s.restore(&e, h, enc)
		out = append(out, chain.Link{Event: e, Hash: h})
	}
	return out, rows.Err()
}

// Range returns events with from <= time < to. Prompt text is decrypted when present.
func (s *Store) Range(from, to time.Time) ([]event.Event, error) {
	rows, err := s.db.Query(`SELECT body, hash, prompt_enc FROM events WHERE ts >= ? AND ts < ? ORDER BY ts`, from.UnixNano(), to.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []event.Event
	for rows.Next() {
		var body, h string
		var enc []byte
		if err := rows.Scan(&body, &h, &enc); err != nil {
			return nil, err
		}
		var e event.Event
		if err := json.Unmarshal([]byte(body), &e); err != nil {
			return nil, err
		}
		s.restore(&e, h, enc)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Scrub deletes the stored prompt text of events older than before. For each such event it
// rewrites the body to its redacted form and drops the encrypted text. Before rewriting a row it
// recomputes that row's chain hash from the full event and refuses to touch a row that does not
// match, so scrubbing can never break verification. It returns how many events were scrubbed.
func (s *Store) Scrub(before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type row struct {
		id   int64
		body string
		hash string
		enc  []byte
	}
	rows, err := s.db.Query(`SELECT id, body, hash, prompt_enc FROM events
		WHERE ts < ? AND (prompt_enc IS NOT NULL OR instr(body, '"prompt_text"') > 0) ORDER BY id`, before.UnixNano())
	if err != nil {
		return 0, err
	}
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.body, &r.hash, &r.enc); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range todo {
		var e event.Event
		if err := json.Unmarshal([]byte(r.body), &e); err != nil {
			return n, err
		}
		s.restore(&e, r.hash, r.enc)
		// The stored hash must be reproducible from the full event; otherwise leave the row alone.
		prev := chain.Genesis
		err := s.db.QueryRow(`SELECT hash FROM events WHERE id < ? ORDER BY id DESC LIMIT 1`, r.id).Scan(&prev)
		if err != nil && err != sql.ErrNoRows {
			return n, err
		}
		h, err := chain.Next(prev, e)
		if err != nil {
			return n, err
		}
		if h != r.hash {
			return n, fmt.Errorf("event %d does not match its chain hash; refusing to scrub", r.id)
		}
		b, _ := json.Marshal(e.Redacted())
		if _, err := s.db.Exec(`UPDATE events SET body = ?, prompt_enc = NULL WHERE id = ?`, string(b), r.id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// DB exposes the handle for tests.
func (s *Store) DB() *sql.DB { return s.db }

// Count returns the number of events in the chain.
func (s *Store) Count() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
	return n, err
}

// LinksFrom returns the chain hash preceding the first event at or after t, and every
// link from there to the end of the chain (in chain order).
func (s *Store) LinksFrom(t time.Time) (string, []chain.Link, error) {
	all, err := s.Links()
	if err != nil {
		return "", nil, err
	}
	prev := chain.Genesis
	for i, l := range all {
		if !l.Event.Time.Before(t) {
			return prev, all[i:], nil
		}
		prev = l.Hash
	}
	return prev, nil, nil
}
