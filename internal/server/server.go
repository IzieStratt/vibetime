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
	DB          *sql.DB
	Now         func() time.Time
	Git         GitEvidence // nil disables git checks
	AuditRate   float64     // fraction of clean submissions still sent to human review
	Rand        func() float64
	Webhook     string // POSTed on review status changes
	OpenSignup  bool
	SyncWindow  time.Duration // how old a synced minute may be (anti-backfill)
	hub         hub
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
	return &Server{DB: db, Now: time.Now, AuditRate: 0.05, SyncWindow: 15 * time.Minute, Rand: randFloat}, nil
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
	m.HandleFunc("POST /api/v1/submissions", s.auth(s.submit))
	m.HandleFunc("GET /api/v1/submissions/{id}", s.auth(s.getSubmission))
	m.HandleFunc("POST /api/v1/submissions/{id}/review", s.auth(s.review))
	m.HandleFunc("GET /api/v1/review/queue", s.auth(s.queue))
	m.HandleFunc("GET /u/{handle}", s.profile)
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
		tx.Exec(`INSERT INTO minutes(user_id, device, project, minute, seconds) VALUES(?,?,?,?,?)
			ON CONFLICT(user_id, device, project, minute) DO UPDATE SET seconds = excluded.seconds`, u.ID, in.Device, m.Project, m.Minute, sec)
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

type hub struct {
	mu   sync.Mutex
	subs map[int64]map[chan struct{}]bool
}

func (h *hub) subscribe(id int64) chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
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
}

// DuplicatePromptMinLen: only distinctive prompts count toward cross-account duplicates.
const DuplicatePromptMinLen = 40

func (s *Server) submit(w http.ResponseWriter, r *http.Request, u user) {
	var in SubmitRequest
	if !decode(w, r, &in, 64<<20) {
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

	// D3: the same distinctive prompts submitted from another account.
	var shas []string
	for _, l := range in.Links {
		e := l.Event.Redacted()
		if e.Kind == event.Prompt && e.Project == in.Project && e.PromptSHA != "" && e.PromptLen >= DuplicatePromptMinLen {
			shas = append(shas, e.PromptSHA)
		}
	}
	dups := 0
	for _, h := range shas {
		var n int
		s.DB.QueryRow(`SELECT COUNT(*) FROM prompt_hashes WHERE sha = ? AND user_id != ?`, h, u.ID).Scan(&n)
		if n > 0 {
			dups++
		}
	}
	if dups > 0 {
		res.Flags = append(res.Flags, verify.Flag{Code: "duplicate_prompts", Weight: 40, Detail: strconv.Itoa(dups) + " prompts match another account's submission"})
		res.Risk += 40
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
	out, err := s.DB.Exec(`INSERT INTO submissions(user_id, device, project, repo, from_ts, to_ts, verified_seconds, approved_seconds, status, risk, result, bundle, created)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, u.ID, in.Device, in.Project, in.Repo, in.From.Unix(), in.To.Unix(), res.VerifiedSeconds, approved, status, res.Risk, string(resJSON), string(bundle), s.Now().Unix())
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	id, _ := out.LastInsertId()
	for _, h := range shas {
		s.DB.Exec(`INSERT INTO prompt_hashes(sha, user_id, submission_id) VALUES(?,?,?)`, h, u.ID, id)
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
