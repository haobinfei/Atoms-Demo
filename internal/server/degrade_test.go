package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
)

// When the primary 生成引擎 (a real DeepSeek HTTP call against a dead
// endpoint) fails, the chain must degrade to the template engine, announce
// it on the timeline, and still land a working app on disk.
func TestSessionDegradesToTemplateProvider(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()
	dir := t.TempDir()
	srv := server.New(server.Config{
		WorkspaceDir: dir,
		DevAuth:      true,
		Prov: provider.Chain{Providers: []provider.Provider{
			provider.NewDeepSeek(dead.URL+"/v1", "key", "m"),
			provider.NewTemplate(),
		}},
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})

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

	events := readSSEUntilDone(t, ts.URL+"/api/sessions/"+out.SessionID+"/events", 0, 40)
	var sawDegrade bool
	for _, ev := range events {
		if ev.Type == "agent" && ev.Agent == "mike" {
			sawDegrade = true
		}
	}
	if !sawDegrade {
		t.Fatalf("degradation not announced on timeline: %+v", events)
	}

	app := filepath.Join(dir, "dev-user", out.SessionID, "app", "src", "App.tsx")
	data, err := os.ReadFile(app)
	if err != nil {
		t.Fatalf("template files not written: %v", err)
	}
	if !strings.Contains(string(data), "购物车") {
		t.Fatal("ecommerce template content missing")
	}
}

// The chain members must be queryable over HTTP so operators can verify
// e.g. that local gemma was probed out at startup.
func TestProvidersEndpointListsChainMembers(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/providers")
	if err != nil {
		t.Fatalf("get providers: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Providers []string `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Providers) != 1 || out.Providers[0] != "fake" {
		t.Fatalf("providers = %v, want [fake]", out.Providers)
	}
}
