// Package api is the local HTTP endpoint that harness plugins and editor plugins talk to.
// It binds to loopback only.
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/IzieStratt/vibetime/internal/attend"
	"github.com/IzieStratt/vibetime/internal/event"
	"github.com/IzieStratt/vibetime/internal/store"
)

const maxBody = 1 << 20

type Server struct {
	Store   *store.Store
	Now     func() time.Time
	MaxSkew time.Duration // 0 = DefaultMaxSkew
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/events", s.events)
	// WakaTime-compatible endpoints so existing editor plugins work unchanged.
	mux.HandleFunc("POST /api/v1/users/current/heartbeats", s.waka)
	mux.HandleFunc("POST /api/v1/users/current/heartbeats.bulk", s.waka)
	// Several editor plugins probe these on startup to check connectivity; without them the
	// plugin shows a connection error even though heartbeats are being accepted.
	mux.HandleFunc("GET /api/v1/users/current", s.wakaUser)
	mux.HandleFunc("GET /api/v1/users/current/statusbar/today", s.wakaToday)
	mux.HandleFunc("GET /v1/summary", s.summary)
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	return guard(mux)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	var evs []event.Event
	if err := json.Unmarshal(raw, &evs); err != nil {
		msg := err.Error()
		// The endpoint takes a JSON array of events; a bare object is the common mistake,
		// so say so instead of surfacing the raw Go unmarshal error.
		if t := bytes.TrimLeft(raw, " \t\r\n"); len(t) > 0 && t[0] == '{' {
			msg = "expected a JSON array of events, e.g. [{...}]; got a single object"
		}
		writeJSON(w, 400, map[string]string{"error": msg})
		return
	}
	for i, e := range evs {
		if err := e.Validate(); err != nil {
			writeJSON(w, 400, map[string]any{"error": err.Error(), "index": i})
			return
		}
		if e.Kind.Internal() {
			writeJSON(w, 400, map[string]any{"error": "presence events are recorded by the agent only", "index": i})
			return
		}
	}
	accepted, stale := 0, 0
	for _, e := range evs {
		if !s.fresh(e) {
			stale++
			continue
		}
		if _, err := s.Store.Append(e); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		accepted++
	}
	writeJSON(w, 202, map[string]any{"accepted": accepted, "stale": stale, "head": s.Store.Head()})
}

type wakaHB struct {
	Time     float64 `json:"time"`
	Project  string  `json:"project"`
	Language string  `json:"language"`
	IsWrite  bool    `json:"is_write"`
	Lines    int     `json:"lines"`
	Category string  `json:"category"`
	AILines  int     `json:"ai_line_changes"`
}

func (s *Server) waka(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, maxBody)
	var raw json.RawMessage
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	var hbs []wakaHB
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &hbs); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
	} else {
		var one wakaHB
		if err := json.Unmarshal(raw, &one); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		hbs = []wakaHB{one}
	}
	resp := make([]any, 0, len(hbs))
	for _, h := range hbs {
		sec := int64(h.Time)
		e := event.Event{
			Time:     time.Unix(sec, int64((h.Time-float64(sec))*1e9)),
			Kind:     event.Heartbeat,
			Project:  h.Project,
			Language: h.Language,
			Harness:  "editor",
		}
		// wakatime-cli also reports AI tools' own activity (category "ai coding", parsed from their
		// transcripts). That is the agent working, not the human: record it as an AI edit,
		// which earns no attended time.
		if strings.EqualFold(h.Category, "ai coding") {
			e.Kind, e.Harness, e.LinesAdded = event.Edit, "wakatime-ai", max(h.AILines, 0)
		}
		if !s.fresh(e) {
			resp = append(resp, []any{map[string]string{"error": "stale timestamp"}, 400})
			continue
		}
		if _, err := s.Store.Append(e); err != nil {
			resp = append(resp, []any{map[string]string{"error": err.Error()}, 400})
			continue
		}
		resp = append(resp, []any{map[string]any{"data": h}, 201})
	}
	writeJSON(w, 202, map[string]any{"responses": resp})
}

// wakaUser answers the WakaTime /users/current probe. The agent has no WakaTime account;
// it returns a static identity so plugins treat the connection as healthy.
func (s *Server) wakaUser(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"data": map[string]any{"id": "vibetime", "username": "vibetime", "display_name": "vibetime"}})
}

// wakaToday answers the status-bar probe with today's attended time in WakaTime's shape.
func (s *Server) wakaToday(w http.ResponseWriter, _ *http.Request) {
	now := s.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	evs, err := s.Store.Range(start, now.Add(time.Second))
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	total := attend.Total(attend.Compute(evs, attend.Default))
	secs := int64(total / time.Second)
	writeJSON(w, 200, map[string]any{"data": map[string]any{
		"grand_total": map[string]any{
			"total_seconds": secs,
			"text":          fmt.Sprintf("%dh %dm", secs/3600, (secs%3600)/60),
		},
	}})
}

// summary returns aggregate attended seconds per project for the last N hours (default 24).
// This is the only shape of data synced to the server: no prompt text, no files.
func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 24*366 {
			writeJSON(w, 400, map[string]string{"error": "bad hours"})
			return
		}
		hours = n
	}
	now := s.Now()
	evs, err := s.Store.Range(now.Add(-time.Duration(hours)*time.Hour), now.Add(time.Second))
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	per := attend.Compute(evs, attend.Default)
	out := map[string]int64{}
	for p, d := range per {
		out[p] = int64(d / time.Second)
	}
	writeJSON(w, 200, map[string]any{"projects": out, "total_seconds": int64(attend.Total(per) / time.Second), "head": s.Store.Head()})
}
