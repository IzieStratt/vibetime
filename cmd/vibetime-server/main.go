// Command vibetime-server runs the central vibetime server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/IzieStratt/vibetime/internal/gitev"
	"github.com/IzieStratt/vibetime/internal/server"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	db := flag.String("db", "vibetime-server.db", "SQLite path")
	openSignup := flag.Bool("open-signup", false, "allow POST /api/v1/signup (dev only; production uses Hack Club login)")
	webhook := flag.String("webhook", "", "URL notified on submission status changes")
	audit := flag.Float64("audit-rate", 0.05, "fraction of clean submissions still sent to human review")
	oauthBase := flag.String("oauth-base", "https://auth.hackclub.com", "OIDC provider base URL; login is enabled when OAUTH_CLIENT_ID is set (uses /oauth/authorize, /oauth/token, /oauth/userinfo); client id/secret from OAUTH_CLIENT_ID/OAUTH_CLIENT_SECRET")
	publicURL := flag.String("public-url", "", "this server's public URL, e.g. https://vibetime.hackclub.com")
	addReviewer := flag.String("add-reviewer", "", "create a reviewer account with this handle, print its token, and exit")
	flag.Parse()

	s, err := server.Open(*db)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	if *addReviewer != "" {
		tok, err := s.CreateUser(*addReviewer, true)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(tok)
		return
	}
	s.OpenSignup, s.Webhook, s.AuditRate, s.PublicURL = *openSignup, *webhook, *audit, *publicURL
	if os.Getenv("OAUTH_CLIENT_ID") != "" && *oauthBase != "" {
		s.OAuth = &server.OAuth{ClientID: os.Getenv("OAUTH_CLIENT_ID"), ClientSecret: os.Getenv("OAUTH_CLIENT_SECRET"),
			AuthorizeURL: *oauthBase + "/oauth/authorize", TokenURL: *oauthBase + "/oauth/token", UserInfoURL: *oauthBase + "/oauth/userinfo",
			RedirectURL: *publicURL + "/auth/callback"}
	}
	s.Git = &gitev.GitHub{Token: os.Getenv("GITHUB_TOKEN")}

	srv := &http.Server{Addr: *addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	log.Printf("vibetime-server on %s", *addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
