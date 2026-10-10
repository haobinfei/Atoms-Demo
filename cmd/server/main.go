package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/haobinfei/Atoms-Demo/internal/auth"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
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
	log.Printf("atoms-demo server listening on %s (workspace=%s, auth=%s)",
		addr, cfg.WorkspaceDir, authMode)
	log.Fatal(http.ListenAndServe(addr, server.New(cfg).Handler()))
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
