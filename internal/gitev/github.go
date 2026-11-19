// Package gitev fetches third-party git evidence. Commit author dates are forgeable, so we
// use GitHub's repository activity feed, whose push timestamps GitHub records itself.
package gitev

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/IzieStratt/vibetime/internal/verify"
)

type GitHub struct {
	BaseURL string // default https://api.github.com
	Token   string // optional, raises rate limits
	HTTP    *http.Client
}

var (
	ErrUnsupported = errors.New("only github.com repositories are supported")
	ErrBadRepo     = errors.New("invalid repository name")
	nameRe         = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// MaxPages bounds how many 100-item activity pages are read per submission.
const MaxPages = 10

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
	// The repo is user-supplied and goes into an authenticated API path: allow only real names.
	if !nameRe.MatchString(parts[1]) || !nameRe.MatchString(parts[2]) || parts[1] == ".." || parts[2] == ".." {
		return nil, ErrBadRepo
	}
	var acts []activity
	// Activity is newest first: page until we pass the start of the window.
	for page := 1; page <= MaxPages; page++ {
		var batch []activity
		if err := g.get(fmt.Sprintf("/repos/%s/%s/activity?per_page=100&page=%d", parts[1], parts[2], page), &batch); err != nil {
			return nil, err
		}
		acts = append(acts, batch...)
		if len(batch) < 100 || batch[len(batch)-1].Timestamp.Before(from) {
			break
		}
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

type activity struct {
	Before    string    `json:"before"`
	After     string    `json:"after"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"activity_type"`
}
