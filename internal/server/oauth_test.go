package server

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// fake OIDC provider
func provider(t *testing.T, sub, name string) *httptest.Server {
	var p *httptest.Server
	p = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authorize":
			q := r.URL.Query()
			if q.Get("client_id") != "cid" || q.Get("response_type") != "code" {
				w.WriteHeader(400)
				return
			}
			http.Redirect(w, r, q.Get("redirect_uri")+"?code=good&state="+url.QueryEscape(q.Get("state")), 302)
		case "/token":
			r.ParseForm()
			if r.Form.Get("code") != "good" || r.Form.Get("client_secret") != "sec" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"access_token": "at"})
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer at" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"sub": sub, "preferred_username": name})
		}
	}))
	t.Cleanup(p.Close)
	return p
}

func (e *env) withOAuth(p *httptest.Server, secret string) {
	e.s.OAuth = &OAuth{ClientID: "cid", ClientSecret: secret, AuthorizeURL: p.URL + "/authorize", TokenURL: p.URL + "/token",
		UserInfoURL: p.URL + "/userinfo", RedirectURL: e.ts.URL + "/auth/callback"}
}

var tokRe = regexp.MustCompile(`-token (vt_[0-9a-f]+)`)

func (e *env) browserLogin() (int, string) {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	r, err := c.Get(e.ts.URL + "/auth/login")
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return r.StatusCode, b.String()
}

func TestOAuthLoginCreatesAccountAndRotatesToken(t *testing.T) {
	e := newEnv(t)
	e.withOAuth(provider(t, "U123", "Rowan Smith!"), "sec")
	code, page := e.browserLogin()
	m := tokRe.FindStringSubmatch(page)
	if code != 200 || m == nil || !strings.Contains(page, "rowan-smith") {
		t.Fatalf("%d %s", code, page)
	}
	if c, _ := e.call("POST", "/api/v1/me", m[1], map[string]bool{"public": true}); c != 200 {
		t.Fatal("token from login doesn't work")
	}
	// second login: same account, new token, old token revoked
	_, page2 := e.browserLogin()
	m2 := tokRe.FindStringSubmatch(page2)
	if m2 == nil || m2[1] == m[1] || !strings.Contains(page2, "rowan-smith") || strings.Contains(page2, "rowan-smith-2") {
		t.Fatal(page2)
	}
	if c, _ := e.call("POST", "/api/v1/me", m[1], map[string]bool{"public": true}); c != 401 {
		t.Fatal("old token still valid")
	}
}

func TestOAuthHandleCollision(t *testing.T) {
	e := newEnv(t)
	e.user("rowan")
	e.withOAuth(provider(t, "U9", "rowan"), "sec")
	if _, page := e.browserLogin(); !strings.Contains(page, "Signed in as rowan-2") {
		t.Fatal(page)
	}
	if e.s.handleFrom("!") != "user" || len(e.s.handleFrom(strings.Repeat("a", 50))) != 24 {
		t.Fatal("handleFrom")
	}
}

func TestOAuthFailures(t *testing.T) {
	e := newEnv(t)
	if c, _ := e.call("GET", "/auth/login", "", nil); c != 404 {
		t.Fatal("unconfigured login")
	}
	if c, _ := e.call("GET", "/auth/callback", "", nil); c != 404 {
		t.Fatal("unconfigured callback")
	}
	p := provider(t, "U1", "x")
	e.withOAuth(p, "wrong-secret")
	if c, _ := e.browserLogin(); c != 502 {
		t.Fatalf("bad secret: %d", c)
	}
	// CSRF: callback without matching state cookie
	if c, _ := e.call("GET", "/auth/callback?code=good&state=abc", "", nil); c != 400 {
		t.Fatal("state not checked")
	}
	e.withOAuth(provider(t, "", "x"), "sec")
	if c, _ := e.browserLogin(); c != 502 {
		t.Fatal("empty sub accepted")
	}
	e.s.OAuth.TokenURL = "http://127.0.0.1:1/token"
	if c, _ := e.browserLogin(); c != 502 {
		t.Fatal("unreachable provider")
	}
}
