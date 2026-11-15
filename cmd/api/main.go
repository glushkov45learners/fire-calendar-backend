// Command api serves the Fire Calendar HTTP API (contract 3).
//
// Environment:
//
//	DATABASE_URL  PostgreSQL connection string (required)
//	ADDR          listen address, default :8080
//	CORS_ORIGINS  comma-separated allowed origins, or *; empty = same-origin only
//
// Monthly digest e-mail (all optional; without a key nothing is mailed):
//
//	SENDGRID_API_KEY  SendGrid API key with the Mail Send permission
//	MAIL_FROM         sender address verified in SendGrid (required with the key)
//	PUBLIC_URL        site origin used in the letter's links,
//	                  default https://glushkov45learners.company
//	DIGEST_KEY        key of the demo page /api/digest?key=...; empty = page is off
package main

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/glushkov45learners/fire-calendar-backend/internal/api"
	"github.com/glushkov45learners/fire-calendar-backend/internal/digest"
	"github.com/glushkov45learners/fire-calendar-backend/internal/mail"
	"github.com/glushkov45learners/fire-calendar-backend/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	var corsOrigins []string
	for _, o := range strings.Split(os.Getenv("CORS_ORIGINS"), ",") {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			corsOrigins = append(corsOrigins, o)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()

	dg := &digest.Service{
		Store:     st,
		PublicURL: strings.TrimRight(cmp.Or(os.Getenv("PUBLIC_URL"), "https://glushkov45learners.company"), "/"),
		Log:       log,
	}
	if key := os.Getenv("SENDGRID_API_KEY"); key != "" {
		from := os.Getenv("MAIL_FROM")
		if from == "" {
			return errors.New("MAIL_FROM is required when SENDGRID_API_KEY is set")
		}
		dg.Mailer = &mail.SendGrid{APIKey: key, From: from, FromName: "Fire Calendar"}
		go dg.Run(ctx)
	} else {
		log.Info("SENDGRID_API_KEY is not set: the monthly digest is not mailed")
	}

	srv := &http.Server{
		Addr: addr,
		Handler: api.New(st, log, api.Config{
			CORSOrigins: corsOrigins,
			Digest:      dg,
			DigestKey:   os.Getenv("DIGEST_KEY"),
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", addr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
