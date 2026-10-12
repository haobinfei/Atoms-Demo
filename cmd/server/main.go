package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/haobinfei/Atoms-Demo/internal/artifact"
	"github.com/haobinfei/Atoms-Demo/internal/eventstream"
	"github.com/haobinfei/Atoms-Demo/internal/identity"
	"github.com/haobinfei/Atoms-Demo/internal/jobqueue"
	"github.com/haobinfei/Atoms-Demo/internal/mailer"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

func main() {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		log.Fatal("DATABASE_DSN is required for identity and session storage")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Fatalf("identity postgres pool: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("identity postgres ping: %v", err)
	}
	ident := identity.New(pool)
	trustedProxies, err := parseTrustedProxyCIDRs(os.Getenv("TRUSTED_PROXY_CIDRS"))
	if err != nil {
		log.Fatalf("trusted proxy configuration: %v", err)
	}
	ident.SetAdminEmails(strings.Split(os.Getenv("PLATFORM_ADMIN_EMAILS"), ","))
	mailCfg, err := mailer.ConfigFromEnv()
	if err != nil {
		log.Fatalf("smtp config: %v", err)
	}
	sender, err := mailer.NewSMTP(mailCfg)
	if err != nil {
		log.Fatalf("smtp sender: %v", err)
	}
	cfg := server.Config{
		WorkspaceDir:      getenv("WORKSPACE_DIR", "workspace"),
		Identity:          ident,
		Mailer:            sender,
		PublicURL:         getenv("APP_BASE_URL", "http://localhost:4173"),
		SecureCookies:     os.Getenv("COOKIE_SECURE") != "0",
		TrustedProxyCIDRs: trustedProxies,
		QueueWorkers:      getenvInt("QUEUE_WORKERS", 3),
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
	store, err := session.NewPGStore(cfg.WorkspaceDir, dsn)
	if err != nil {
		log.Fatalf("postgres session store: %v", err)
	}
	defer store.Close()
	cfg.Store = store
	artifacts, artifactMode := newArtifacts()
	cfg.Artifacts = artifacts
	bus, busMode := newEventBus()
	cfg.Bus = bus
	queue, queueMode := newJobQueue(cfg.QueueWorkers)
	cfg.Queue = queue
	addr := ":" + getenv("PORT", "4173")
	log.Printf("atoms-demo server listening on %s (workspace=%s, auth=identity, store=postgres, artifacts=%s, bus=%s, queue=%s, workers=%d)",
		addr, cfg.WorkspaceDir, artifactMode, busMode, queueMode, cfg.QueueWorkers)
	srv := server.New(cfg)
	defer srv.Close()
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}

func parseTrustedProxyCIDRs(raw string) ([]netip.Prefix, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var prefixes []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

// newArtifacts wires the optional COS build-output store. All credentials
// come from the atoms-demo-env Secret; partial configuration fails fast so a
// typo cannot silently fall back to replica-local dist files.
func newArtifacts() (*artifact.Store, string) {
	bucket := strings.TrimSpace(os.Getenv("COS_BUCKET"))
	endpoint := strings.TrimSpace(os.Getenv("COS_ENDPOINT"))
	secretID := strings.TrimSpace(os.Getenv("COS_SECRET_ID"))
	secretKey := strings.TrimSpace(os.Getenv("COS_SECRET_KEY"))
	sessionToken := strings.TrimSpace(os.Getenv("COS_SESSION_TOKEN"))
	if bucket == "" && endpoint == "" && secretID == "" && secretKey == "" && sessionToken == "" {
		return nil, "local"
	}
	store, err := artifact.NewCOS(artifact.COSConfig{
		Bucket:       bucket,
		Endpoint:     endpoint,
		SecretID:     secretID,
		SecretKey:    secretKey,
		SessionToken: sessionToken,
	})
	if err != nil {
		log.Fatalf("COS artifact store: %v", err)
	}
	return store, "cos:" + bucket
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

// newJobQueue wires the 任务 (Job) Queue. Redis is the production backend
// (ADR-0007): when REDIS_HOST is set the queue is a Redis Stream, and an
// unreachable Redis surfaces as an Enqueue error (HTTP 503) rather than a
// silent fallback to a lossy in-process channel. An empty host keeps the
// bounded in-process MemoryQueue for local dev and tests.
func newJobQueue(workers int) (jobqueue.Queue, string) {
	host := os.Getenv("REDIS_HOST")
	if host == "" {
		return nil, "memory"
	}
	client := redis.NewClient(&redis.Options{
		Addr:     net.JoinHostPort(host, getenv("REDIS_PORT", "6379")),
		Username: os.Getenv("REDIS_USERNAME"),
		Password: os.Getenv("REDIS_PASSWORD"),
	})
	return jobqueue.NewRedisQueue(client, jobqueue.WithCapacity(workers*4)), "redis"
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}
