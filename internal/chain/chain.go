// Package chain implements the tamper-evident hash chain over local events.
// hash_n = sha256(hash_{n-1} || canonical JSON of event_n). The head is periodically
// committed to the server so history cannot be rewritten or backfilled later.
package chain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/IzieStratt/vibetime/internal/event"
)

// Genesis is the hash before any event.
var Genesis = hex.EncodeToString(make([]byte, sha256.Size))

// Next returns the chain hash after appending e to prev.
func Next(prev string, e event.Event) (string, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Link is an event with its resulting chain hash.
type Link struct {
	Event event.Event `json:"event"`
	Hash  string      `json:"hash"`
}

// Verify checks that links form a valid chain starting from prev; returns the index of
// the first bad link or -1.
func Verify(prev string, links []Link) (int, error) {
	for i, l := range links {
		h, err := Next(prev, l.Event)
		if err != nil {
			return i, err
		}
		if h != l.Hash {
			return i, fmt.Errorf("hash mismatch at %d", i)
		}
		prev = h
	}
	return -1, nil
}
