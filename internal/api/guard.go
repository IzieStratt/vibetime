package api

import (
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/IzieStratt/vibetime/internal/event"
)

// DefaultMaxSkew bounds how far an event's claimed time may be from the agent's clock.
// Without it a script could backfill months of fake history in one request.
const DefaultMaxSkew = 5 * time.Minute

func (s *Server) maxSkew() time.Duration {
	if s.MaxSkew > 0 {
		return s.MaxSkew
	}
	return DefaultMaxSkew
}

// fresh reports whether e's timestamp is close enough to now to be trusted.
func (s *Server) fresh(e event.Event) bool {
	d := s.Now().Sub(e.Time)
	return d <= s.maxSkew() && d >= -time.Minute
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// guard blocks browser-originated attacks on the loopback API:
//   - DNS rebinding: the Host header must be a loopback name/IP.
//   - Cross-site requests: any non-loopback Origin is refused, and POSTs must be
//     application/json (a "non-simple" type, so browsers must preflight, which we never approve).
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if err != nil || !loopbackHost(u.Host) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		if r.Method == http.MethodPost {
			mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if mt != "application/json" {
				http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
