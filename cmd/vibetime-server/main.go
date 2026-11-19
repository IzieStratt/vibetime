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
	s.OpenSignup, s.Webhook, s.AuditRate = *openSignup, *webhook, *audit
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
