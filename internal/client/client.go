// Package client sends events to the local agent, spooling to disk when it is unreachable
// so hooks never block or fail the harness.
package client

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/IzieStratt/vibetime/internal/event"
)

type Client struct {
	BaseURL string
	Spool   string // path of JSONL spool file
	HTTP    *http.Client
}

func New(base, spool string) *Client {
	return &Client{BaseURL: base, Spool: spool, HTTP: &http.Client{Timeout: 500 * time.Millisecond}}
}

func (c *Client) post(evs []event.Event) error {
	b, _ := json.Marshal(evs)
	r, err := c.HTTP.Post(c.BaseURL+"/v1/events", "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode >= 500 {
		return fmt.Errorf("agent status %d", r.StatusCode)
	}
	return nil // 4xx = invalid event, dropping is correct
}

// Send flushes any spooled events then sends evs; on failure everything is spooled.
func (c *Client) Send(evs []event.Event) error {
	all := append(c.readSpool(), evs...)
	if len(all) == 0 {
		return nil
	}
	if err := c.post(all); err != nil {
		return c.writeSpool(all)
	}
	os.Remove(c.Spool)
	return nil
}

func (c *Client) readSpool() []event.Event {
	f, err := os.Open(c.Spool)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []event.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e event.Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

func (c *Client) writeSpool(evs []event.Event) error {
	tmp := c.Spool + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, e := range evs {
		enc.Encode(e)
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, c.Spool)
}
