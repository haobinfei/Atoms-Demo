package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/haobinfei/Atoms-Demo/internal/auth"
	"github.com/haobinfei/Atoms-Demo/internal/eventstream"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

func main() {
	authMode := getenv("AUTH_MODE", "dev")
	if authMode != "dev" && authMode != "oidc" {
		log.Fatalf("AUTH_MODE must be \"dev\" or \"oidc\", got %q", authMode)
	}
	cfg := server.Config{
		WorkspaceDir: getenv("WORKSPACE_DIR", "workspace"),
		DevAuth:      authMode == "dev",
		DevUser:      getenv("DEV_USER", "dev-user"),
		Env: provider.EnvConfig{
			DeepSeekAPIKey:  os.Getenv("DEEPSEEK_API_KEY"),
			DeepSeekBaseURL: os.Getenv("DEEPSEEK_BASE_URL"),
			DeepSeekModel:   os.Getenv("DEEPSEEK_MODEL"),
			KimiAPIKey:      os.Getenv("KIMI_API_KEY"),
			KimiBaseURL:     os.Getenv("KIMI_BASE_URL"),
			KimiModel:       os.Getenv("KIMI_MODEL"),
			LocalBaseURL:    getenv("LOCAL_LLM_BASE_URL", "http://192.168.1.101:8090/v1"),
			LocalModel:      os.Getenv("LOCAL_LLM_MODEL"),
		},
	}
	storeMode := "file"
	if dsn := os.Getenv("DATABASE_DSN"); dsn != "" {
		store, err := session.NewPGStore(cfg.WorkspaceDir, dsn)
		if err != nil {
			log.Fatalf("postgres store: %v", err)
		}
		cfg.Store = store
		storeMode = "postgres"
	}
	bus, busMode := newEventBus()
	cfg.Bus = bus
	if authMode == "oidc" {
		mw, err := auth.New(context.Background(), auth.Config{
			Issuer:          getenv("OIDC_ISSUER", "https://auth.ss-vip.cn"),
			ClientID:        os.Getenv("OIDC_CLIENT_ID"),
			ClientSecret:    os.Getenv("OIDC_CLIENT_SECRET"),
			RedirectURL:     getenv("OIDC_REDIRECT_URL", "http://localhost:4173/api/auth/callback"),
			InsecureCookies: os.Getenv("OIDC_INSECURE_COOKIES") == "1",
			SkipIssuerCheck: os.Getenv("OIDC_SKIP_ISSUER_CHECK") == "1",
		})
		if err != nil {
			log.Fatalf("auth middleware: %v", err)
		}
		cfg.Auth = mw
	}
	addr := ":" + getenv("PORT", "4173")
	log.Printf("atoms-demo server listening on %s (workspace=%s, auth=%s, store=%s, bus=%s)",
		addr, cfg.WorkspaceDir, authMode, storeMode, busMode)
	srv := server.New(cfg)
	defer srv.Close()
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}

// newEventBus wires the SSE fan-out from the atoms-middleware Redis Secret
// contract (REDIS_HOST/PORT/USERNAME/PASSWORD). Redis is a hot path, not the
// source of truth (ADR-0007): an empty host keeps the in-process MemoryBus,
// and an unreachable Redis degrades to it without refusing to start —
// clients still catch up through Postgres replay.
func newEventBus() (eventstream.Bus, string) {
	host := os.Getenv("REDIS_HOST")
	if host == "" {
		return nil, "memory"
	}
	client := redis.NewClient(&redis.Options{
		Addr:     net.JoinHostPort(host, getenv("REDIS_PORT", "6379")),
		Username: os.Getenv("REDIS_USERNAME"),
		Password: os.Getenv("REDIS_PASSWORD"),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		log.Printf("redis unavailable (%v); SSE fan-out degrades to Postgres replay", err)
		return eventstream.NewFanoutBus(client), "redis (degraded)"
	}
	return eventstream.NewFanoutBus(client), "redis"
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
