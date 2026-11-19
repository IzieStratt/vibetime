package syncer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/IzieStratt/vibetime/internal/server"
)

// HackatimeURL is Hackatime's WakaTime-compatible API base.
const HackatimeURL = "https://hackatime.hackclub.com/api/hackatime/v1"

// FetchHackatime reads daily per-project totals from Hackatime between from and to
// (inclusive), in chunks of at most 366 days. The API key is sent only to Hackatime.
func FetchHackatime(base, key string, from, to time.Time, hc *http.Client) ([]server.ImportDay, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	var out []server.ImportDay
	for start := from; !start.After(to); start = start.AddDate(0, 0, 366) {
		end := start.AddDate(0, 0, 365)
		if end.After(to) {
			end = to
		}
		q := url.Values{"start": {start.Format("2006-01-02")}, "end": {end.Format("2006-01-02")}}
		req, _ := http.NewRequest("GET", base+"/users/current/summaries?"+q.Encode(), nil)
		req.Header.Set("Authorization", "Bearer "+key)
		r, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		var body struct {
			Data []struct {
				Range struct {
					Date string `json:"date"`
				} `json:"range"`
				Projects []struct {
					Name         string  `json:"name"`
					TotalSeconds float64 `json:"total_seconds"`
				} `json:"projects"`
			} `json:"data"`
		}
		err = json.NewDecoder(r.Body).Decode(&body)
		r.Body.Close()
		if r.StatusCode != 200 {
			return nil, fmt.Errorf("hackatime: status %d", r.StatusCode)
		}
		if err != nil {
			return nil, fmt.Errorf("hackatime: %v", err)
		}
		for _, d := range body.Data {
			for _, p := range d.Projects {
				if p.TotalSeconds >= 1 && p.Name != "" {
					out = append(out, server.ImportDay{Date: d.Range.Date, Project: p.Name, Seconds: int64(p.TotalSeconds)})
				}
			}
		}
	}
	return out, nil
}

// ImportHackatime fetches history and uploads it to the vibetime server as unverified.
func (s *Syncer) ImportHackatime(base, key string, from, to time.Time) (int, error) {
	days, err := FetchHackatime(base, key, from, to, s.HTTP)
	if err != nil {
		return 0, err
	}
	var res struct{ Imported int }
	for i := 0; i < len(days); i += 5000 {
		chunk := days[i:min(i+5000, len(days))]
		var r struct{ Imported int }
		if err := s.post("/api/v1/import", server.ImportRequest{Source: "hackatime", Days: chunk}, &r); err != nil {
			return res.Imported, err
		}
		res.Imported += r.Imported
	}
	return res.Imported, nil
}
