package main

import (
	"context"
	"log"
	"net/http"
	"net/netip"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/haobinfei/Atoms-Demo/internal/identity"
	"github.com/haobinfei/Atoms-Demo/internal/mailer"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

func main() {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		log.Fatal("DATABASE_DSN is required for E2E")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("E2E Postgres unavailable: %v", err)
	}
	store, err := session.NewPGStore("e2e/.workspace", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	sender, err := mailer.NewSMTP(mailer.Config{
		Host: "127.0.0.1", Port: 51025, From: "noreply@localhost", Security: "none",
	})
	if err != nil {
		log.Fatal(err)
	}
	srv := server.New(server.Config{
		WorkspaceDir:  "e2e/.workspace",
		Identity:      identity.New(pool),
		Mailer:        sender,
		PublicURL:     "http://127.0.0.1:5199",
		SecureCookies: false,
		// Playwright gives each browser context its own test client IP, so
		// repeated suites retain the real shared limiter without exhausting
		// one loopback client's quota. This trust is E2E-only.
		TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		Prov:              provider.NewTemplate(),
		Store:             store,
	})
	defer srv.Close()
	log.Fatal(http.ListenAndServe("127.0.0.1:4173", srv.Handler()))
}
