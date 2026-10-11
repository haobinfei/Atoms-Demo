package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
)

// A model failure must be surfaced instead of being hidden by a template
// fallback. The turn is marked failed, the error is visible on the timeline,
// and no stale or synthetic app is written.
func TestSessionFailsWhenModelGenerationFails(t *testing.T) {
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
	var sawError bool
	for _, ev := range events {
		if ev.Type == "error" {
			message, _ := ev.Payload["message"].(string)
			if strings.Contains(message, "status 500") {
				sawError = true
			}
		}
	}
	if !sawError {
		t.Fatalf("model error not announced on timeline: %+v", events)
	}

	get, err := http.Get(ts.URL + "/api/sessions/" + out.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	defer get.Body.Close()
	var sess struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(get.Body).Decode(&sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if sess.Status != "failed" {
		t.Fatalf("session status = %q, want failed", sess.Status)
	}
	if _, err := os.Stat(dir + "/dev-user/" + out.SessionID + "/app/src/App.tsx"); !os.IsNotExist(err) {
		t.Fatalf("fallback app should not be written, stat err = %v", err)
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
