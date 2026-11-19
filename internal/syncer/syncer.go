// Package syncer pushes the chain head (a live commitment) and per-minute attended time
// to the vibetime server, and builds submission bundles.
package syncer

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/IzieStratt/vibetime/internal/attend"
	"github.com/IzieStratt/vibetime/internal/event"
	"github.com/IzieStratt/vibetime/internal/server"
	"github.com/IzieStratt/vibetime/internal/store"
)

// Config is persisted in <data dir>/config.json (mode 0600).
type Config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
	Device string `json:"device"`
}

func LoadConfig(dir string) (Config, error) {
	var c Config
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

func SaveConfig(dir string, c Config) error {
	if c.Device == "" {
		var b [12]byte
		rand.Read(b[:])
		c.Device = "dev-" + hex.EncodeToString(b[:])
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600)
}

type Syncer struct {
	Store *store.Store
	Cfg   Config
	HTTP  *http.Client
	Now   func() time.Time
}

func (s *Syncer) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (s *Syncer) post(path string, body, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", s.Cfg.Server+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Cfg.Token)
	r, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode >= 300 {
		var e struct{ Error string }
		json.NewDecoder(r.Body).Decode(&e)
		return fmt.Errorf("server %d: %s", r.StatusCode, e.Error)
	}
	if out != nil {
		return json.NewDecoder(r.Body).Decode(out)
	}
	return nil
}

// Tick sends the current head and the last 15 minutes of attended time.
func (s *Syncer) Tick() error {
	now := s.Now()
	evs, err := s.Store.Range(now.Add(-30*time.Minute), now.Add(time.Minute))
	if err != nil {
		return err
	}
	n, _ := s.Store.Count()
	req := server.SyncRequest{Device: s.Cfg.Device, Head: s.Store.Head(), Count: n}
	oldest := now.Add(-14 * time.Minute).Truncate(time.Minute).Unix()
	type key struct {
		p string
		m int64
	}
	agg := map[key]*server.SyncMinute{}
	get := func(p string, m int64) *server.SyncMinute {
		k := key{p, m}
		if agg[k] == nil {
			agg[k] = &server.SyncMinute{Project: p, Minute: m}
		}
		return agg[k]
	}
	for p, mins := range attend.MinutesByMode(evs, attend.Default) {
		for m, mm := range mins {
			sm := get(p, m)
			sm.Seconds = int((mm[0] + mm[1] + mm[2]) / time.Second)
			sm.HandsOn, sm.Steering, sm.Watching = int(mm[attend.HandsOn]/time.Second), int(mm[attend.Steering]/time.Second), int(mm[attend.Watching]/time.Second)
		}
	}
	for _, e := range evs {
		if e.Project == "" {
			continue
		}
		m := e.Time.Truncate(time.Minute).Unix()
		switch {
		case e.Kind == event.Prompt:
			get(e.Project, m).Prompts++
		case e.Kind == event.Edit && e.Human:
			get(e.Project, m).HumanEdits++
		case e.Kind == event.Edit:
			get(e.Project, m).AILines += e.LinesAdded + e.LinesRemoved
		}
	}
	for _, sm := range agg {
		if sm.Minute >= oldest && sm.Minute <= now.Unix() {
			req.Minutes = append(req.Minutes, *sm)
		}
	}
	return s.post("/api/v1/sync", req, nil)
}

// Run ticks every interval until stop is closed.
func (s *Syncer) Run(interval time.Duration, stop <-chan struct{}, onErr func(error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := s.Tick(); err != nil && onErr != nil {
			onErr(err)
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// Submit builds a bundle for project in [from, to] and sends it. Prompt text is redacted
// (only its hash and length are sent) unless includePrompts is set.
func (s *Syncer) Submit(project, repo string, from, to time.Time, includePrompts bool) (server.Submission, error) {
	var out server.Submission
	prev, links, err := s.Store.LinksFrom(from.Add(-10 * time.Minute))
	if err != nil {
		return out, err
	}
	var keep = links[:0:0]
	for _, l := range links {
		if l.Event.Time.After(to.Add(10 * time.Minute)) {
			break
		}
		// other projects are needed to verify the chain, but never with prompt text
		if !includePrompts || l.Event.Project != project {
			l.Event = l.Event.Redacted()
		}
		keep = append(keep, l)
	}
	if len(keep) == 0 {
		return out, fmt.Errorf("no events between %s and %s", from, to)
	}
	err = s.post("/api/v1/submissions", server.SubmitRequest{Device: s.Cfg.Device, Project: project, Repo: repo, From: from, To: to, Prev: prev, Links: keep}, &out)
	return out, err
}

// Login creates an account (open-signup servers) and saves the config.
func Login(dir, serverURL, handle string, httpc *http.Client) (Config, error) {
	b, _ := json.Marshal(map[string]string{"handle": handle})
	if httpc == nil {
		httpc = &http.Client{Timeout: 20 * time.Second}
	}
	r, err := httpc.Post(serverURL+"/api/v1/signup", "application/json", bytes.NewReader(b))
	if err != nil {
		return Config{}, err
	}
	defer r.Body.Close()
	var out struct{ Token, Error string }
	json.NewDecoder(r.Body).Decode(&out)
	if r.StatusCode != 201 {
		return Config{}, fmt.Errorf("signup failed (%d): %s", r.StatusCode, out.Error)
	}
	if err := SaveConfig(dir, Config{Server: serverURL, Token: out.Token}); err != nil {
		return Config{}, err
	}
	return LoadConfig(dir)
}
