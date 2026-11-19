// Package gitev fetches third-party git evidence. Commit author dates are forgeable, so we
// use GitHub's repository activity feed, whose push timestamps GitHub records itself.
package gitev

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/IzieStratt/vibetime/internal/verify"
)

type GitHub struct {
	BaseURL string // default https://api.github.com
	Token   string // optional, raises rate limits
	HTTP    *http.Client
}

var ErrUnsupported = errors.New("only github.com repositories are supported")

func (g *GitHub) get(path string, v any) error {
	base := g.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	req, _ := http.NewRequest("GET", base+path, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	c := g.HTTP
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	r, err := c.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("github %s: %d", path, r.StatusCode)
	}
	return json.NewDecoder(r.Body).Decode(v)
}

// Commits returns pushes to repo ("github.com/owner/name") between from and to.
func (g *GitHub) Commits(repo string, from, to time.Time) ([]verify.Commit, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 3 || parts[0] != "github.com" {
		return nil, ErrUnsupported
	}
	var acts []struct {
		Before    string    `json:"before"`
		After     string    `json:"after"`
		Timestamp time.Time `json:"timestamp"`
		Type      string    `json:"activity_type"`
	}
	if err := g.get(fmt.Sprintf("/repos/%s/%s/activity?per_page=100", parts[1], parts[2]), &acts); err != nil {
		return nil, err
	}
	out := []verify.Commit{}
	for _, a := range acts {
		if a.Timestamp.Before(from) || a.Timestamp.After(to) {
			continue
		}
		if a.Type != "push" && a.Type != "force_push" && a.Type != "branch_creation" {
			continue
		}
		c := verify.Commit{SHA: a.After, PushedAt: a.Timestamp}
		if a.Before != "" && strings.Trim(a.Before, "0") != "" {
			var cmp struct {
				Files []struct{ Additions, Deletions int } `json:"files"`
			}
			if err := g.get(fmt.Sprintf("/repos/%s/%s/compare/%s...%s", parts[1], parts[2], a.Before, a.After), &cmp); err == nil {
				for _, f := range cmp.Files {
					c.Additions += f.Additions
					c.Deletions += f.Deletions
				}
			}
		}
		out = append(out, c)
	}
	return out, nil
}
