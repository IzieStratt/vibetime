// Package store persists the local event chain in SQLite.
package store

import (
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"github.com/IzieStratt/vibetime/internal/chain"
	"github.com/IzieStratt/vibetime/internal/event"
	_ "modernc.org/sqlite"
)

type Store struct {
	mu   sync.Mutex
	db   *sql.DB
	head string
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
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
	s := &Store{db: db, head: chain.Genesis}
	if err := db.QueryRow(`SELECT hash FROM events ORDER BY id DESC LIMIT 1`).Scan(&s.head); err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Append validates, chains and stores an event, returning the new head.
func (s *Store) Append(e event.Event) (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	e.Time = e.Time.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := chain.Next(s.head, e)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(e)
	if _, err := s.db.Exec(`INSERT INTO events(ts, body, hash) VALUES(?,?,?)`, e.Time.UnixNano(), string(b), h); err != nil {
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

// Links returns all chain links in insertion order.
func (s *Store) Links() ([]chain.Link, error) {
	rows, err := s.db.Query(`SELECT body, hash FROM events ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chain.Link
	for rows.Next() {
		var body, h string
		if err := rows.Scan(&body, &h); err != nil {
			return nil, err
		}
		var e event.Event
		if err := json.Unmarshal([]byte(body), &e); err != nil {
			return nil, err
		}
		out = append(out, chain.Link{Event: e, Hash: h})
	}
	return out, rows.Err()
}

// Range returns events with from <= time < to.
func (s *Store) Range(from, to time.Time) ([]event.Event, error) {
	rows, err := s.db.Query(`SELECT body FROM events WHERE ts >= ? AND ts < ? ORDER BY ts`, from.UnixNano(), to.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []event.Event
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var e event.Event
		if err := json.Unmarshal([]byte(body), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
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
