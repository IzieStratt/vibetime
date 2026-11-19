package server

import (
	_ "embed"
	"net/http"
)

//go:embed console.html
var consoleHTML []byte

// console serves the reviewer UI. It is static: all data comes from the token-authed API,
// so the page itself reveals nothing.
func (s *Server) console(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Write(consoleHTML)
}
