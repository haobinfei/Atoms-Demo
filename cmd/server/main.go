package main

import (
	"log"
	"net/http"
	"os"

	"github.com/haobinfei/Atoms-Demo/internal/server"
)

func main() {
	cfg := server.Config{
		WorkspaceDir: getenv("WORKSPACE_DIR", "workspace"),
		DevAuth:      getenv("AUTH_MODE", "dev") == "dev",
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
