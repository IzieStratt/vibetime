// Package server is the central vibetime server: accounts, live sync with chain
// commitments, cross-device stats, public profiles, submissions and review.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IzieStratt/vibetime/internal/chain"
	"github.com/IzieStratt/vibetime/internal/event"
	"github.com/IzieStratt/vibetime/internal/verify"
	_ "modernc.org/sqlite"
)

// GitEvidence supplies third-party push records (gitev.GitHub in production).
type GitEvidence interface {
	Commits(repo string, from, to time.Time) ([]verify.Commit, error)
}

type Server struct {
	path        string
	DB          *sql.DB
	Now         func() time.Time
	Git         GitEvidence // nil disables git checks
	AuditRate   float64     // fraction of clean submissions still sent to human review
	Rand        func() float64
	Webhook     string // POSTed on review status changes
	OpenSignup  bool
	SyncWindow  time.Duration // how old a synced minute may be (anti-backfill)
	OAuth       *OAuth        // nil disables provider login
	PublicURL   string        // e.g. https://vibetime.hackclub.com
	hub         hub
	rlMu        sync.Mutex
	recent      map[int64][]time.Time
	webhookWait sync.WaitGroup
}

const schema = `
CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY, handle TEXT UNIQUE NOT NULL, token_sha TEXT UNIQUE NOT NULL,
  reviewer INTEGER NOT NULL DEFAULT 0, public INTEGER NOT NULL DEFAULT 1, created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS commitments (user_id INTEGER NOT NULL, device TEXT NOT NULL, head TEXT NOT NULL,
  count INTEGER NOT NULL, received_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS commitments_dev ON commitments(user_id, device, received_at);
CREATE TABLE IF NOT EXISTS minutes (user_id INTEGER NOT NULL, device TEXT NOT NULL, project TEXT NOT NULL,
  minute INTEGER NOT NULL, seconds INTEGER NOT NULL, PRIMARY KEY (user_id, device, project, minute));
CREATE INDEX IF NOT EXISTS minutes_user ON minutes(user_id, minute);
CREATE TABLE IF NOT EXISTS submissions (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, device TEXT NOT NULL,
  project TEXT NOT NULL, repo TEXT NOT NULL, from_ts INTEGER NOT NULL, to_ts INTEGER NOT NULL,
  verified_seconds INTEGER NOT NULL, approved_seconds INTEGER, status TEXT NOT NULL, risk INTEGER NOT NULL,
  result TEXT NOT NULL, bundle TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS prompt_hashes (sha TEXT NOT NULL, user_id INTEGER NOT NULL, submission_id INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS prompt_hashes_sha ON prompt_hashes(sha);
CREATE TABLE IF NOT EXISTS credited (user_id INTEGER NOT NULL, minute INTEGER NOT NULL, seconds INTEGER NOT NULL, submission_id INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS credited_user ON credited(user_id, minute);
`

func Open(path string) (*Server, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	db.Exec(`ALTER TABLE users ADD COLUMN oauth_sub TEXT`) // migration; errors when already present
	for _, c := range []string{"email TEXT", "slack_id TEXT", "verification_status TEXT", "ysws_eligible INTEGER"} {
		db.Exec(`ALTER TABLE users ADD COLUMN ` + c)
	}
	for _, c := range []string{"prompts", "ai_lines", "human_edits", "hands_on", "steering", "watching"} {
		db.Exec(`ALTER TABLE minutes ADD COLUMN ` + c + ` INTEGER NOT NULL DEFAULT 0`)
	}
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS users_oauth ON users(oauth_sub)`)
	if _, err := db.Exec(importSchema); err != nil {
		return nil, err
	}
	return &Server{path: path, DB: db, Now: time.Now, AuditRate: 0.05, SyncWindow: 15 * time.Minute, Rand: randFloat}, nil
}

func randFloat() float64 {
	var b [8]byte
	rand.Read(b[:])
	return float64(uint64(b[0])<<48|uint64(b[1])<<40|uint64(b[2])<<32|uint64(b[3])<<24|uint64(b[4])<<16|uint64(b[5])<<8|uint64(b[6])) / float64(uint64(1)<<56)
}

func (s *Server) Close() error { s.webhookWait.Wait(); return s.DB.Close() }

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /api/v1/signup", s.signup)
	m.HandleFunc("POST /api/v1/sync", s.auth(s.sync))
	m.HandleFunc("POST /api/v1/me", s.auth(s.updateMe))
	m.HandleFunc("GET /api/v1/users/{handle}/stats", s.stats)
	m.HandleFunc("GET /api/v1/users/{handle}/live", s.live)
	m.HandleFunc("GET /api/v1/users/{handle}/insights", s.insights)
	m.HandleFunc("POST /api/v1/submissions", s.auth(s.submit))
	m.HandleFunc("POST /api/v1/import", s.auth(s.importHistory))
	m.HandleFunc("GET /api/v1/submissions/{id}", s.auth(s.getSubmission))
	m.HandleFunc("POST /api/v1/submissions/{id}/review", s.auth(s.review))
	m.HandleFunc("GET /api/v1/review/queue", s.auth(s.queue))
	m.HandleFunc("GET /u/{handle}", s.profile)
	m.HandleFunc("GET /review", s.console)
	m.HandleFunc("GET /auth/login", s.oauthStart)
	m.HandleFunc("GET /auth/callback", s.oauthCallback)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	return m
}

// ---- helpers

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decode(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		fail(w, 400, err.Error())
		return false
	}
	return true
}

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

type user struct {
	ID       int64
	Handle   string
	Reviewer bool
	Public   bool
}

type authed func(http.ResponseWriter, *http.Request, user)

func (s *Server) auth(h authed) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || tok == "" {
			fail(w, 401, "missing token")
			return
		}
		var u user
		err := s.DB.QueryRow(`SELECT id, handle, reviewer, public FROM users WHERE token_sha = ?`, sha(tok)).Scan(&u.ID, &u.Handle, &u.Reviewer, &u.Public)
		if err != nil {
			fail(w, 401, "bad token")
			return
		}
		h(w, r, u)
	}
}

var handleRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,31}$`)
var deviceRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// ---- accounts

// CreateUser makes an account and returns its API token (also used by the admin CLI).
func (s *Server) CreateUser(handle string, reviewer bool) (string, error) {
	if !handleRe.MatchString(handle) {
		return "", errors.New("handle must be 2-32 chars of a-z 0-9 _ -")
	}
	var b [24]byte
	rand.Read(b[:])
	tok := "vt_" + hex.EncodeToString(b[:])
	_, err := s.DB.Exec(`INSERT INTO users(handle, token_sha, reviewer, created) VALUES(?,?,?,?)`, handle, sha(tok), reviewer, s.Now().Unix())
	if err != nil {
		return "", errors.New("handle taken")
	}
	return tok, nil
}

func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	if !s.OpenSignup {
		fail(w, 403, "signup is via Hack Club login")
		return
	}
	var in struct{ Handle string }
	if !decode(w, r, &in, 1<<10) {
		return
	}
	tok, err := s.CreateUser(in.Handle, false)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]string{"handle": in.Handle, "token": tok})
}

func (s *Server) updateMe(w http.ResponseWriter, r *http.Request, u user) {
	var in struct{ Public *bool }
	if !decode(w, r, &in, 1<<10) {
		return
	}
	if in.Public != nil {
		s.DB.Exec(`UPDATE users SET public = ? WHERE id = ?`, *in.Public, u.ID)
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) lookup(handle string) (user, bool) {
	var u user
	err := s.DB.QueryRow(`SELECT id, handle, reviewer, public FROM users WHERE handle = ?`, handle).Scan(&u.ID, &u.Handle, &u.Reviewer, &u.Public)
	return u, err == nil
}

// ---- sync (live commitments + minute aggregates)

type SyncMinute struct {
	Project string `json:"project"`
	Minute  int64  `json:"minute"` // unix seconds, minute aligned
	Seconds int    `json:"seconds"`
	// Steering counts for the minute (aggregates only, no content).
	Prompts    int `json:"prompts,omitempty"`
	AILines    int `json:"ai_lines,omitempty"`
	HumanEdits int `json:"human_edits,omitempty"`
	// How the attended seconds split (hands-on, steering, watching the agent).
	HandsOn  int `json:"hands_on,omitempty"`
	Steering int `json:"steering,omitempty"`
	Watching int `json:"watching,omitempty"`
}

type SyncRequest struct {
	Device  string       `json:"device"`
	Head    string       `json:"head"`
	Count   int          `json:"count"`
	Minutes []SyncMinute `json:"minutes"`
}

func (s *Server) sync(w http.ResponseWriter, r *http.Request, u user) {
	var in SyncRequest
	if !decode(w, r, &in, 1<<20) {
		return
	}
	if !deviceRe.MatchString(in.Device) || len(in.Head) != 64 {
		fail(w, 400, "bad device or head")
		return
	}
	now := s.Now()
	tx, err := s.DB.Begin()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer tx.Rollback()
	// D1: the server's receive time is the anchor for everything this head covers.
	tx.Exec(`INSERT INTO commitments(user_id, device, head, count, received_at) VALUES(?,?,?,?,?)`, u.ID, in.Device, in.Head, in.Count, now.UnixNano())
	accepted, rejected := 0, 0
	oldest := now.Add(-s.SyncWindow).Truncate(time.Minute).Unix()
	for _, m := range in.Minutes {
		// A2: minutes may only be reported while they are fresh; history can't be backfilled.
		if m.Minute%60 != 0 || m.Minute < oldest || m.Minute > now.Unix() || m.Project == "" || len(m.Project) > 200 {
			rejected++
			continue
		}
		sec := min(max(m.Seconds, 0), 60)
		clamp := func(n int) int { return min(max(n, 0), 10000) }
		tx.Exec(`INSERT INTO minutes(user_id, device, project, minute, seconds, prompts, ai_lines, human_edits, hands_on, steering, watching) VALUES(?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(user_id, device, project, minute) DO UPDATE SET seconds = excluded.seconds, prompts = excluded.prompts,
			ai_lines = excluded.ai_lines, human_edits = excluded.human_edits, hands_on = excluded.hands_on, steering = excluded.steering,
			watching = excluded.watching`, u.ID, in.Device, m.Project, m.Minute, sec, clamp(m.Prompts), clamp(m.AILines), clamp(m.HumanEdits),
			min(max(m.HandsOn, 0), 60), min(max(m.Steering, 0), 60), min(max(m.Watching, 0), 60))
		accepted++
	}
	if err := tx.Commit(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	s.hub.publish(u.ID)
	writeJSON(w, 200, map[string]int{"accepted": accepted, "rejected": rejected})
}

// ---- stats (cross-device merge)

type Stats struct {
	Handle       string           `json:"handle"`
	TodaySeconds int64            `json:"today_seconds"`
	WeekSeconds  int64            `json:"week_seconds"`
	Projects     map[string]int64 `json:"projects"` // last 30 days
}

// UserStats merges devices: a minute can hold at most 60s per user no matter how many
// devices or projects reported it (B4). Overlapping minutes are split proportionally.
func (s *Server) UserStats(u user) (Stats, error) {
	now := s.Now().UTC()
	since := now.Add(-30 * 24 * time.Hour).Unix()
	rows, err := s.DB.Query(`SELECT project, minute, seconds FROM minutes WHERE user_id = ? AND minute >= ?`, u.ID, since)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()
	type pm struct {
		project string
		sec     int64
	}
	perMin := map[int64][]pm{}
	for rows.Next() {
		var p string
		var m, sec int64
		rows.Scan(&p, &m, &sec)
		perMin[m] = append(perMin[m], pm{p, sec})
	}
	st := Stats{Handle: u.Handle, Projects: map[string]int64{}}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Unix()
	week := now.Add(-7 * 24 * time.Hour).Unix()
	frac := map[string]float64{}
	for m, list := range perMin {
		var total int64
		for _, x := range list {
			total += x.sec
		}
		capped := min(total, 60)
		if m >= today {
			st.TodaySeconds += capped
		}
		if m >= week {
			st.WeekSeconds += capped
		}
		for _, x := range list {
			if total > 0 {
				frac[x.project] += float64(x.sec) * float64(capped) / float64(total)
			}
		}
	}
	for p, f := range frac {
		st.Projects[p] = int64(f + 0.5)
	}
	return st, rows.Err()
}

func (s *Server) visible(w http.ResponseWriter, r *http.Request) (user, bool) {
	u, ok := s.lookup(r.PathValue("handle"))
	if !ok || !u.Public {
		fail(w, 404, "no such public user")
		return u, false
	}
	return u, true
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	u, ok := s.visible(w, r)
	if !ok {
		return
	}
	st, err := s.UserStats(u)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, st)
}

// live streams stats over SSE whenever the user syncs.
func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	u, ok := s.visible(w, r)
	if !ok {
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch := s.hub.subscribe(u.ID)
	if ch == nil {
		fail(w, 503, "too many live viewers for this profile")
		return
	}
	defer s.hub.unsubscribe(u.ID, ch)
	send := func() {
		st, _ := s.UserStats(u)
		b, _ := json.Marshal(st)
		w.Write([]byte("data: " + string(b) + "\n\n"))
		fl.Flush()
	}
	send()
	keep := time.NewTicker(25 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			send()
		case <-keep.C:
			w.Write([]byte(": keepalive\n\n"))
			fl.Flush()
		}
	}
}

// MaxLiveViewers caps open live streams per profile.
const MaxLiveViewers = 50

type hub struct {
	mu   sync.Mutex
	subs map[int64]map[chan struct{}]bool
}

func (h *hub) subscribe(id int64) chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs[id]) >= MaxLiveViewers {
		return nil
	}
	if h.subs == nil {
		h.subs = map[int64]map[chan struct{}]bool{}
	}
	if h.subs[id] == nil {
		h.subs[id] = map[chan struct{}]bool{}
	}
	ch := make(chan struct{}, 1)
	h.subs[id][ch] = true
	return ch
}

func (h *hub) unsubscribe(id int64, ch chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs[id], ch)
}

func (h *hub) publish(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[id] {
		select {
		case ch <- struct{}{}:
		default: // already pending
		}
	}
}

// ---- submissions

type SubmitRequest struct {
	Device  string       `json:"device"`
	Project string       `json:"project"`
	Repo    string       `json:"repo"`
	From    time.Time    `json:"from"`
	To      time.Time    `json:"to"`
	Prev    string       `json:"prev"`
	Links   []chain.Link `json:"links"`
}

type Submission struct {
	ID              int64         `json:"id"`
	Handle          string        `json:"handle"`
	Project         string        `json:"project"`
	Repo            string        `json:"repo"`
	From            time.Time     `json:"from"`
	To              time.Time     `json:"to"`
	Status          string        `json:"status"` // auto_approved | needs_review | approved | partial | rejected
	VerifiedSeconds int64         `json:"verified_seconds"`
	ApprovedSeconds *int64        `json:"approved_seconds"`
	Risk            int           `json:"risk"`
	Result          verify.Result `json:"result"`
	Note            string        `json:"note"`
	Links           []chain.Link  `json:"links,omitempty"`
	// Unified carries what a Unified Projects DB record needs (code URL, approved hours,
	// justification, review link); programs map these keys onto their Airtable fields.
	Unified UnifiedRecord `json:"unified"`
}

type UnifiedRecord struct {
	CodeURL        string   `json:"code_url"`
	HoursSpent     *float64 `json:"hours_spent"` // approved hours; null until reviewed
	Justification  string   `json:"hours_justification"`
	ReviewURL      string   `json:"review_url"`
	VerifiedHours  float64  `json:"verified_hours"`
	VibetimeStatus string   `json:"status"`
	// Airtable holds the same data under the Unified YSWS DB's own field names (as programs
	// such as Flavortown write them), ready to send as-is.
	Airtable map[string]any `json:"airtable"`
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request, u user) {
	var in SubmitRequest
	if !decode(w, r, &in, 32<<20) {
		return
	}
	if !deviceRe.MatchString(in.Device) || in.Project == "" || !in.To.After(in.From) || len(in.Links) == 0 {
		fail(w, 400, "device, project, from<to and links are required")
		return
	}
	if in.To.Sub(in.From) > 366*24*time.Hour {
		fail(w, 400, "window too long")
		return
	}
	if len(in.Links) > MaxBundleLinks {
		fail(w, 413, "bundle too large; submit a shorter window")
		return
	}
	if !s.allowSubmit(u.ID) {
		fail(w, 429, "too many submissions; try again in a minute")
		return
	}
	rows, err := s.DB.Query(`SELECT head, received_at FROM commitments WHERE user_id = ? AND device = ?`, u.ID, in.Device)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	var cs []verify.Commitment
	for rows.Next() {
		var c verify.Commitment
		var ns int64
		rows.Scan(&c.Head, &ns)
		c.ReceivedAt = time.Unix(0, ns)
		cs = append(cs, c)
	}
	rows.Close()

	q := verify.Request{Project: in.Project, Repo: in.Repo, From: in.From, To: in.To, Prev: in.Prev, Links: in.Links, Commitments: cs}
	if s.Git != nil && in.Repo != "" {
		q.Commits, q.GitErr = s.Git.Commits(in.Repo, in.From, in.To.Add(24*time.Hour))
	}
	res := verify.Verify(q)
	// Hack Club Auth says whether the person can take part in YSWS programs (age, verification).
	var eligible sql.NullBool
	s.DB.QueryRow(`SELECT ysws_eligible FROM users WHERE id = ?`, u.ID).Scan(&eligible)
	if eligible.Valid && !eligible.Bool {
		// Block auto-approval: programs decide eligibility, but a flagged-ineligible account
		// should never slip through unreviewed.
		res.Flags = append(res.Flags, verify.Flag{Code: "not_ysws_eligible", Weight: 25, Detail: "Hack Club Auth reports this account is not YSWS eligible"})
		res.Risk += 25
	}


	// Everything that reads credited and then writes submission + credited must be one
	// transaction, or concurrent submits of the same window each see 0 already-credited.
	tx, err := s.DB.Begin()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer tx.Rollback()

	// B4 / review #2: a user's minute can be credited once, across all devices and submissions
	// (rejected submissions release their minutes).
	if removed := s.dedupe(tx, u.ID, &res); removed > 0 {
		w := 0
		if removed*10 > res.VerifiedSeconds+removed {
			w = 20
		}
		res.Flags = append(res.Flags, verify.Flag{Code: "already_credited", Weight: w,
			Detail: fmt.Sprintf("%.1fh of this window was already credited to another submission or device; not counted again", float64(removed)/3600)})
		res.Risk += w
	}
	status := "needs_review"
	if res.Valid && res.Risk == 0 {
		status = "auto_approved"
		if s.Rand() < s.AuditRate {
			status = "needs_review"
			res.Flags = append(res.Flags, verify.Flag{Code: "random_audit", Detail: "randomly sampled for human review"})
		}
	}
	resJSON, _ := json.Marshal(res)
	bundle, _ := json.Marshal(in.Links)
	var approved any
	if status == "auto_approved" {
		approved = res.VerifiedSeconds
	}
	out, err := tx.Exec(`INSERT INTO submissions(user_id, device, project, repo, from_ts, to_ts, verified_seconds, approved_seconds, status, risk, result, bundle, created)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, u.ID, in.Device, in.Project, in.Repo, in.From.Unix(), in.To.Unix(), res.VerifiedSeconds, approved, status, res.Risk, string(resJSON), string(bundle), s.Now().Unix())
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	id, _ := out.LastInsertId()
	for m, sec := range res.Minutes {
		if sec > 0 {
			tx.Exec(`INSERT INTO credited(user_id, minute, seconds, submission_id) VALUES(?,?,?,?)`, u.ID, m, sec, id)
		}
	}
	if err := tx.Commit(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	sub, _ := s.loadSubmission(id, false)
	s.notify(sub)
	writeJSON(w, 201, sub)
}

func (s *Server) loadSubmission(id int64, withBundle bool) (Submission, int64) {
	var sub Submission
	var uid, from, to int64
	var approved sql.NullInt64
	var resJSON, bundle string
	err := s.DB.QueryRow(`SELECT s.id, u.handle, s.user_id, s.project, s.repo, s.from_ts, s.to_ts, s.status, s.verified_seconds, s.approved_seconds, s.risk, s.result, s.bundle, s.note
		FROM submissions s JOIN users u ON u.id = s.user_id WHERE s.id = ?`, id).Scan(&sub.ID, &sub.Handle, &uid, &sub.Project, &sub.Repo, &from, &to, &sub.Status, &sub.VerifiedSeconds, &approved, &sub.Risk, &resJSON, &bundle, &sub.Note)
	if err != nil {
		return sub, 0
	}
	sub.From, sub.To = time.Unix(from, 0).UTC(), time.Unix(to, 0).UTC()
	if approved.Valid {
		sub.ApprovedSeconds = &approved.Int64
	}
	json.Unmarshal([]byte(resJSON), &sub.Result)
	sub.Unified = s.unified(sub, uid)
	if withBundle {
		json.Unmarshal([]byte(bundle), &sub.Links)
	}
	return sub, uid
}

func (s *Server) getSubmission(w http.ResponseWriter, r *http.Request, u user) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	sub, owner := s.loadSubmission(id, true)
	if owner == 0 || (owner != u.ID && !u.Reviewer) {
		fail(w, 404, "not found")
		return
	}
	writeJSON(w, 200, sub)
}

func (s *Server) queue(w http.ResponseWriter, r *http.Request, u user) {
	if !u.Reviewer {
		fail(w, 403, "reviewers only")
		return
	}
	rows, err := s.DB.Query(`SELECT id FROM submissions WHERE status = 'needs_review' ORDER BY risk DESC, id LIMIT 100`)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	out := []Submission{}
	for _, id := range ids {
		sub, _ := s.loadSubmission(id, false)
		out = append(out, sub)
	}
	writeJSON(w, 200, out)
}

func (s *Server) review(w http.ResponseWriter, r *http.Request, u user) {
	if !u.Reviewer {
		fail(w, 403, "reviewers only")
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	sub, owner := s.loadSubmission(id, false)
	if owner == 0 {
		fail(w, 404, "not found")
		return
	}
	if owner == u.ID {
		fail(w, 403, "cannot review your own submission")
		return
	}
	var in struct {
		Status          string
		ApprovedSeconds int64 `json:"approved_seconds"`
		Note            string
	}
	if !decode(w, r, &in, 1<<16) {
		return
	}
	switch in.Status {
	case "approved":
		in.ApprovedSeconds = sub.VerifiedSeconds
	case "rejected":
		in.ApprovedSeconds = 0
	case "partial":
		if in.ApprovedSeconds < 0 || in.ApprovedSeconds > sub.VerifiedSeconds {
			fail(w, 400, "approved_seconds must be between 0 and verified_seconds")
			return
		}
	default:
		fail(w, 400, "status must be approved, partial or rejected")
		return
	}
	s.DB.Exec(`UPDATE submissions SET status = ?, approved_seconds = ?, note = ? WHERE id = ?`, in.Status, in.ApprovedSeconds, in.Note, id)
	sub, _ = s.loadSubmission(id, false)
	s.notify(sub)
	writeJSON(w, 200, sub)
}

// notify posts the submission to the program webhook (YSWS / Unified Projects DB).
func (s *Server) notify(sub Submission) {
	if s.Webhook == "" {
		return
	}
	b, _ := json.Marshal(sub)
	s.webhookWait.Add(1)
	go func() {
		defer s.webhookWait.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "POST", s.Webhook, strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		if r, err := http.DefaultClient.Do(req); err == nil {
			r.Body.Close()
		}
	}()
}

func (s *Server) dbPath() string { return s.path }

// PublicURL is used to build review links in exported records.
func (s *Server) unified(sub Submission, uid int64) UnifiedRecord {
	rec := UnifiedRecord{VerifiedHours: round2(float64(sub.VerifiedSeconds) / 3600), VibetimeStatus: sub.Status,
		ReviewURL: s.PublicURL + fmt.Sprintf("/review#%d", sub.ID)}
	if sub.Repo != "" {
		rec.CodeURL = "https://" + sub.Repo
	}
	if sub.ApprovedSeconds != nil {
		h := round2(float64(*sub.ApprovedSeconds) / 3600)
		rec.HoursSpent = &h
		var flags []string
		for _, f := range sub.Result.Flags {
			if f.Weight > 0 {
				flags = append(flags, f.Code)
			}
		}
		rec.Justification = fmt.Sprintf("vibetime: %.2fh verified from live-anchored activity (%d events); status %s", rec.VerifiedHours, sub.Result.Anchored, sub.Status)
		if len(flags) > 0 {
			rec.Justification += "; flags: " + strings.Join(flags, ", ")
		}
		if sub.Note != "" {
			rec.Justification += "; reviewer: " + sub.Note
		}
	}
	var email, slack sql.NullString
	s.DB.QueryRow(`SELECT email, slack_id FROM users WHERE id = ?`, uid).Scan(&email, &slack)
	rec.Airtable = map[string]any{"Code URL": rec.CodeURL}
	if email.String != "" {
		rec.Airtable["Email"] = email.String
	}
	if slack.String != "" {
		rec.Airtable["slack_id"] = slack.String
	}
	if rec.HoursSpent != nil {
		rec.Airtable["Optional - Override Hours Spent"] = *rec.HoursSpent
		rec.Airtable["Optional - Override Hours Spent Justification"] = rec.Justification + " (" + rec.ReviewURL + ")"
	}
	return rec
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

// querier is satisfied by both *sql.DB and *sql.Tx so dedupe can read inside a transaction.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// dedupe trims res.Minutes so no minute exceeds 60s across the user's live submissions,
// updates VerifiedSeconds, and returns the seconds removed. It must read and the caller must
// write the credited rows inside the same transaction, or two concurrent submits both see 0.
func (s *Server) dedupe(q querier, uid int64, res *verify.Result) int64 {
	if len(res.Minutes) == 0 {
		return 0
	}
	lo, hi := int64(1<<62), int64(0)
	for m := range res.Minutes {
		lo, hi = min(lo, m), max(hi, m)
	}
	rows, err := q.Query(`SELECT c.minute, SUM(c.seconds) FROM credited c JOIN submissions s ON s.id = c.submission_id
		WHERE c.user_id = ? AND c.minute BETWEEN ? AND ? AND s.status != 'rejected' GROUP BY c.minute`, uid, lo, hi)
	if err != nil {
		return 0
	}
	used := map[int64]int64{}
	for rows.Next() {
		var m, sec int64
		rows.Scan(&m, &sec)
		used[m] = sec
	}
	rows.Close()
	var removed int64
	for m, sec := range res.Minutes {
		// Overlapping claims of a minute are most likely the same activity, so only time beyond
		// what is already credited counts, and never past 60s.
		if keep := min(max(sec-used[m], 0), max(60-used[m], 0)); keep < sec {
			removed += sec - keep
			res.Minutes[m] = keep
		}
	}
	res.VerifiedSeconds -= removed
	return removed
}

// MaxBundleLinks bounds verification work per submission (~ a year of heavy use).
const MaxBundleLinks = 300_000

// SubmitBurst submissions are allowed per user per minute.
const SubmitBurst = 5

func (s *Server) allowSubmit(uid int64) bool {
	s.rlMu.Lock()
	defer s.rlMu.Unlock()
	if s.recent == nil {
		s.recent = map[int64][]time.Time{}
	}
	now := s.Now()
	keep := s.recent[uid][:0]
	for _, t := range s.recent[uid] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) >= SubmitBurst {
		s.recent[uid] = keep
		return false
	}
	s.recent[uid] = append(keep, now)
	return true
}
