package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// OAuth configures login through an OAuth2/OIDC provider (Hack Club's identity
// provider in production). Accounts are keyed by the provider's stable subject id.
type OAuth struct {
	ClientID     string
	ClientSecret string
	AuthorizeURL string
	TokenURL     string
	UserInfoURL  string
	RedirectURL  string // https://<host>/auth/callback
	Scope        string // default "openid profile"
	HTTP         *http.Client
}

const stateCookie = "vt_oauth_state"

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	o := s.OAuth
	if o == nil {
		fail(w, 404, "login not configured")
		return
	}
	var b [16]byte
	rand.Read(b[:])
	state := hex.EncodeToString(b[:])
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: state, Path: "/auth", HttpOnly: true, Secure: strings.HasPrefix(o.RedirectURL, "https://"),
		SameSite: http.SameSiteLaxMode, MaxAge: 600})
	scope := o.Scope
	if scope == "" {
		scope = "openid profile email slack_id verification_status" // all allowed for Hack Club Auth community apps
	}
	q := url.Values{"response_type": {"code"}, "client_id": {o.ClientID}, "redirect_uri": {o.RedirectURL}, "scope": {scope}, "state": {state}}
	http.Redirect(w, r, o.AuthorizeURL+"?"+q.Encode(), http.StatusFound)
}

var nonHandle = regexp.MustCompile(`[^a-z0-9_-]+`)

// handleFrom derives a valid, unused handle from the provider's name.
func (s *Server) handleFrom(name string) string {
	h := strings.Trim(nonHandle.ReplaceAllString(strings.ToLower(name), "-"), "-_")
	if len(h) > 24 {
		h = h[:24]
	}
	if len(h) < 2 {
		h = "user"
	}
	for i := 0; ; i++ {
		c := h
		if i > 0 {
			c = fmt.Sprintf("%s-%d", h, i+1)
		}
		if _, taken := s.lookup(c); !taken {
			return c
		}
	}
}

var tokenPage = template.Must(template.New("t").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>vibetime login</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:600px;margin:40px auto;padding:0 16px;background:#fbfaf7;color:#1d1b18}
@media (prefers-color-scheme:dark){body{background:#151412;color:#ece8e1}}code{display:block;padding:12px;border:1px solid #8884;border-radius:8px;word-break:break-all}</style>
</head><body><h1>Signed in as {{.Handle}}</h1><p>Run this on each computer you build on:</p>
<code>vibetime login -server {{.Server}} -token {{.Token}}</code>
<p>Keep this token private. Signing in again issues a new token and revokes this one.</p>
<p><a href="/u/{{.Handle}}">Your public profile</a></p></body></html>`))

func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	o := s.OAuth
	if o == nil {
		fail(w, 404, "login not configured")
		return
	}
	c, err := r.Cookie(stateCookie)
	if err != nil || c.Value == "" || c.Value != r.URL.Query().Get("state") {
		fail(w, 400, "bad state")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: "/auth", MaxAge: -1})
	code := r.URL.Query().Get("code")
	if code == "" {
		fail(w, 400, "missing code")
		return
	}
	hc := o.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {o.RedirectURL},
		"client_id": {o.ClientID}, "client_secret": {o.ClientSecret}}
	tr, err := hc.PostForm(o.TokenURL, form)
	if err != nil {
		fail(w, 502, "token exchange failed")
		return
	}
	defer tr.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if tr.StatusCode != 200 || json.NewDecoder(tr.Body).Decode(&tok) != nil || tok.AccessToken == "" {
		fail(w, 502, "token exchange failed")
		return
	}
	req, _ := http.NewRequest("GET", o.UserInfoURL, nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	ur, err := hc.Do(req)
	if err != nil {
		fail(w, 502, "userinfo failed")
		return
	}
	defer ur.Body.Close()
	var info struct {
		Sub               string `json:"sub"`
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
		SlackID           string `json:"slack_id"`
		Verification      string `json:"verification_status"`
		YSWSEligible      *bool  `json:"ysws_eligible"`
		Nickname          string `json:"nickname"`
		Name              string `json:"name"`
	}
	if ur.StatusCode != 200 || json.NewDecoder(ur.Body).Decode(&info) != nil || info.Sub == "" {
		fail(w, 502, "userinfo failed")
		return
	}
	var b [24]byte
	rand.Read(b[:])
	apiTok := "vt_" + hex.EncodeToString(b[:])
	var handle string
	if err := s.DB.QueryRow(`SELECT handle FROM users WHERE oauth_sub = ?`, info.Sub).Scan(&handle); err == nil {
		s.DB.Exec(`UPDATE users SET token_sha = ? WHERE oauth_sub = ?`, sha(apiTok), info.Sub)
		s.saveIdentity(info.Sub, info.Email, info.SlackID, info.Verification, info.YSWSEligible)
	} else {
		name := info.PreferredUsername
		for _, n := range []string{info.Nickname, info.Name} {
			if name == "" {
				name = n
			}
		}
		handle = s.handleFrom(name)
		if _, err := s.DB.Exec(`INSERT INTO users(handle, token_sha, created, oauth_sub) VALUES(?,?,?,?)`, handle, sha(apiTok), s.Now().Unix(), info.Sub); err != nil {
			fail(w, 500, "could not create account")
			return
		}
		s.saveIdentity(info.Sub, info.Email, info.SlackID, info.Verification, info.YSWSEligible)
	}
	server := strings.TrimSuffix(strings.TrimSuffix(o.RedirectURL, "/auth/callback"), "/")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	tokenPage.Execute(w, map[string]string{"Handle": handle, "Server": server, "Token": apiTok})
}

// saveIdentity stores the Hack Club identity fields programs need for the Unified DB.
// They are private: never shown on profiles, only in submissions to reviewers and webhooks.
func (s *Server) saveIdentity(sub, email, slackID, verification string, eligible *bool) {
	var el any
	if eligible != nil {
		el = *eligible
	}
	s.DB.Exec(`UPDATE users SET email = ?, slack_id = ?, verification_status = ?, ysws_eligible = ? WHERE oauth_sub = ?`,
		email, slackID, verification, el, sub)
}
