// Package control holds the user's local controls: folders to exclude, a pause, and optional
// forwarding of WakaTime heartbeats to a previous endpoint. It lives in control.json next to
// the agent's other state, and the agent re-reads it on every request so CLI changes apply
// without a restart.
package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const fileName = "control.json"

// Settings is the persisted control state. The zero value means: nothing excluded, not paused,
// no forwarding.
type Settings struct {
	Excluded []string `json:"excluded,omitempty"`
	// PausedUntil, when in the future, drops all non-presence events.
	PausedUntil time.Time `json:"paused_until,omitzero"`
	// ForwardURL receives a copy of each WakaTime-compatible heartbeat body (off when empty).
	ForwardURL string `json:"forward_url,omitempty"`
	// ForwardKey is sent as HTTP Basic auth (base64 of the key, WakaTime style) when set.
	ForwardKey string `json:"forward_key,omitempty"`
	// PromptRetentionDays: prompt text older than this is scrubbed from the local database (the
	// chain keeps its hashes). nil means DefaultRetentionDays; 0 keeps prompts forever.
	PromptRetentionDays *int `json:"prompt_retention_days,omitempty"`
	// ChangedAt is when exclusions or a pause last changed. Presence is withheld for a minute
	// after that, because a presence event carries no folder of its own.
	ChangedAt time.Time `json:"changed_at,omitzero"`
}

// DefaultRetentionDays is how long prompt text is kept when the user has not chosen otherwise.
const DefaultRetentionDays = 14

// Retention returns the prompt retention in days (0 = keep forever).
func (s Settings) Retention() int {
	if s.PromptRetentionDays == nil {
		return DefaultRetentionDays
	}
	return *s.PromptRetentionDays
}

// ChangedWithin reports whether exclusions or the pause changed less than d before now.
func (s Settings) ChangedWithin(now time.Time, d time.Duration) bool {
	return !s.ChangedAt.IsZero() && now.Sub(s.ChangedAt) < d
}

// Paused reports whether events are being dropped at now.
func (s Settings) Paused(now time.Time) bool { return now.Before(s.PausedUntil) }

// Excludes reports whether cwd is one of the excluded folders or inside one.
func (s Settings) Excludes(cwd string) bool {
	if cwd == "" {
		return false
	}
	c := clean(cwd)
	for _, f := range s.Excluded {
		if c == f || strings.HasPrefix(c, f+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Store reads and writes control.json under Dir. It also remembers the working directory of the
// most recent ingested event, so presence events (which carry no project) can be gated.
type Store struct {
	Dir string
	mu  sync.Mutex
	// last is the cwd of the most recent event that carried one ("" if none yet).
	last string
}

// Load returns the current settings. A missing file is not an error.
func (s *Store) Load() (Settings, error) {
	var out Settings
	b, err := os.ReadFile(filepath.Join(s.Dir, fileName))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}

// Save writes the settings atomically with owner-only permissions.
func (s *Store) Save(cfg Settings) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.Dir, fileName+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.Dir, fileName))
}

// update applies fn under the store lock.
func (s *Store) update(fn func(*Settings) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.Load()
	if err != nil {
		return err
	}
	if err := fn(&cfg); err != nil {
		return err
	}
	return s.Save(cfg)
}

// Exclude adds folder to the excluded list. Paths are stored absolute and cleaned.
func (s *Store) Exclude(folder string) (string, error) {
	p, err := Normalize(folder)
	if err != nil {
		return "", err
	}
	return p, s.update(func(c *Settings) error {
		for _, f := range c.Excluded {
			if f == p {
				return nil
			}
		}
		c.Excluded = append(c.Excluded, p)
		c.ChangedAt = time.Now()
		return nil
	})
}

// Include removes folder from the excluded list. It reports whether anything was removed.
func (s *Store) Include(folder string) (bool, error) {
	p, err := Normalize(folder)
	if err != nil {
		return false, err
	}
	removed := false
	err = s.update(func(c *Settings) error {
		var keep []string
		for _, f := range c.Excluded {
			if f == p {
				removed = true
				continue
			}
			keep = append(keep, f)
		}
		c.Excluded = keep
		if removed {
			c.ChangedAt = time.Now()
		}
		return nil
	})
	return removed, err
}

// Pause sets PausedUntil.
func (s *Store) Pause(until time.Time) error {
	return s.update(func(c *Settings) error { c.PausedUntil, c.ChangedAt = until, time.Now(); return nil })
}

// Resume clears any pause.
func (s *Store) Resume() error {
	return s.update(func(c *Settings) error { c.PausedUntil, c.ChangedAt = time.Time{}, time.Now(); return nil })
}

// SetForward sets (or with an empty url, clears) the heartbeat forwarding target.
func (s *Store) SetForward(rawURL, key string) error {
	if rawURL != "" {
		u, err := url.Parse(rawURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("forward url must be an http(s) URL, got %q", rawURL)
		}
	}
	return s.update(func(c *Settings) error { c.ForwardURL, c.ForwardKey = rawURL, key; return nil })
}

// SetRetention sets how many days of prompt text are kept (0 = forever).
func (s *Store) SetRetention(days int) error {
	if days < 0 {
		return fmt.Errorf("retention must be 0 (keep forever) or a positive number of days, got %d", days)
	}
	return s.update(func(c *Settings) error { c.PromptRetentionDays = &days; return nil })
}

// NoteCwd records the cwd of the most recent event that carried one.
func (s *Store) NoteCwd(cwd string) {
	if cwd == "" {
		return
	}
	s.mu.Lock()
	s.last = cwd
	s.mu.Unlock()
}

// LastCwd returns the cwd most recently noted by NoteCwd.
func (s *Store) LastCwd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// Normalize returns the absolute, cleaned, symlink-resolved form of a folder path.
func Normalize(folder string) (string, error) {
	if strings.TrimSpace(folder) == "" {
		return "", errors.New("folder is required")
	}
	abs, err := filepath.Abs(folder)
	if err != nil {
		return "", err
	}
	return clean(abs), nil
}

// clean returns p with symlinks resolved in its longest existing prefix, so /tmp and
// /private/tmp compare equal even for folders that no longer exist.
func clean(p string) string {
	p = filepath.Clean(p)
	var rest []string
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			parts := append([]string{r}, rest...)
			return filepath.Join(parts...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = parent
	}
}

// ParsePause turns a pause duration into an absolute end time. It accepts 30m, 1h, 2h (any Go
// duration) or "tomorrow" (local midnight).
func ParsePause(arg string, now time.Time) (time.Time, error) {
	if arg == "tomorrow" {
		y, m, d := now.Date()
		return time.Date(y, m, d+1, 0, 0, 0, 0, now.Location()), nil
	}
	d, err := time.ParseDuration(arg)
	if err != nil || d <= 0 {
		return time.Time{}, fmt.Errorf("pause: want a duration like 30m, 1h, 2h or tomorrow; got %q", arg)
	}
	return now.Add(d), nil
}
