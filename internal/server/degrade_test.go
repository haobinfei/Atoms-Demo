package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
)

type alwaysBroken struct{}

func (alwaysBroken) Name() string { return "broken-deepseek" }
func (alwaysBroken) Generate(context.Context, string, func(string, string)) (provider.FileSet, error) {
	return nil, errors.New("connection refused (simulated)")
}

// When the primary 生成引擎 fails, the chain must degrade to the template
// engine, announce it on the timeline, and still land a working app on disk.
func TestSessionDegradesToTemplateProvider(t *testing.T) {
	dir := t.TempDir()
	srv := server.New(server.Config{
		WorkspaceDir: dir,
		DevAuth:      true,
		Prov:         provider.Chain{Providers: []provider.Provider{alwaysBroken{}, provider.NewTemplate()}},
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/sessions", "application/json", strings.NewReader(`{"prompt":"一家咖啡店的电商网站"}`))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()

	events := readSSEUntilDone(t, ts.URL+"/api/sessions/"+out.SessionID+"/events", 40)
	var sawDegrade bool
	for _, ev := range events {
		if ev.Type == "agent" && ev.Agent == "mike" {
			sawDegrade = true
		}
	}
	if !sawDegrade {
		t.Fatalf("degradation not announced on timeline: %+v", events)
	}

	app := filepath.Join(dir, out.SessionID, "app", "src", "App.tsx")
	data, err := os.ReadFile(app)
	if err != nil {
		t.Fatalf("template files not written: %v", err)
	}
	if !strings.Contains(string(data), "购物车") {
		t.Fatal("ecommerce template content missing")
	}
}
