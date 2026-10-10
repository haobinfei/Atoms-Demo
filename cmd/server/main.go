package main

import (
	"log"
	"net/http"
	"os"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
)

func main() {
	cfg := server.Config{
		WorkspaceDir: getenv("WORKSPACE_DIR", "workspace"),
		DevAuth:      getenv("AUTH_MODE", "dev") == "dev",
		Env: provider.EnvConfig{
			DeepSeekAPIKey:  os.Getenv("DEEPSEEK_API_KEY"),
			DeepSeekBaseURL: os.Getenv("DEEPSEEK_BASE_URL"),
			DeepSeekModel:   os.Getenv("DEEPSEEK_MODEL"),
			LocalBaseURL:    getenv("LOCAL_LLM_BASE_URL", "http://192.168.1.101:8090/v1"),
			LocalModel:      os.Getenv("LOCAL_LLM_MODEL"),
		},
	}
	addr := ":" + getenv("PORT", "4173")
	log.Printf("atoms-demo server listening on %s (workspace=%s, auth=%s)",
		addr, cfg.WorkspaceDir, map[bool]string{true: "dev", false: "oidc"}[cfg.DevAuth])
	log.Fatal(http.ListenAndServe(addr, server.New(cfg).Handler()))
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
