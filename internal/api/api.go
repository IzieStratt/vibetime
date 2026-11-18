// Package api is the local HTTP endpoint that harness plugins and editor plugins talk to.
// It binds to loopback only.
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/IzieStratt/vibetime/internal/attend"
	"github.com/IzieStratt/vibetime/internal/event"
	"github.com/IzieStratt/vibetime/internal/store"
)

const maxBody = 1 << 20

type Server struct {
	Store *store.Store
	Now   func() time.Time
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/events", s.events)
	// WakaTime-compatible endpoints so existing editor plugins work unchanged.
	mux.HandleFunc("POST /api/v1/users/current/heartbeats", s.waka)
	mux.HandleFunc("POST /api/v1/users/current/heartbeats.bulk", s.waka)
	mux.HandleFunc("GET /v1/summary", s.summary)
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	var evs []event.Event
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&evs); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	for i, e := range evs {
		if _, err := s.Store.Append(e); err != nil {
			writeJSON(w, 400, map[string]any{"error": err.Error(), "index": i})
			return
		}
	}
	writeJSON(w, 202, map[string]any{"accepted": len(evs), "head": s.Store.Head()})
}

type wakaHB struct {
	Time     float64 `json:"time"`
	Project  string  `json:"project"`
	Language string  `json:"language"`
	IsWrite  bool    `json:"is_write"`
	Lines    int     `json:"lines"`
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
		if _, err := s.Store.Append(e); err != nil {
			resp = append(resp, []any{map[string]string{"error": err.Error()}, 400})
			continue
		}
		resp = append(resp, []any{map[string]any{"data": h}, 201})
	}
	writeJSON(w, 202, map[string]any{"responses": resp})
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
