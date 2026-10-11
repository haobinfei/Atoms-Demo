package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
)

func newTestServer(t *testing.T) *httptest.Server {
	return newTestServerWith(t, provider.FakeProvider{})
}

// newTestServerWith injects a specific 生成引擎; the template provider is
// the deterministic choice when the test asserts on prompt-dependent output
// (e.g. accent color after a chat turn).
func newTestServerWith(t *testing.T, prov provider.Provider) *httptest.Server {
	ts, _ := newTestServerDir(t, prov)
	return ts
}

// newTestServerDir additionally exposes the workspace directory so tests
// can inspect or corrupt on-disk state.
func newTestServerDir(t *testing.T, prov provider.Provider) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	srv := server.New(server.Config{WorkspaceDir: dir, DevAuth: true, Prov: prov})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	return ts, dir
}

func TestCreateSessionPersistsWorkspace(t *testing.T) {
	ts := newTestServer(t)

	body, _ := json.Marshal(map[string]string{"prompt": "一家咖啡店的电商网站"})
	resp, err := http.Post(ts.URL+"/api/sessions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/sessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.SessionID == "" {
		t.Fatal("sessionId is empty")
	}

	// The session must be recoverable from disk via GET /api/sessions/{id}.
	get, err := http.Get(ts.URL + "/api/sessions/" + out.SessionID)
	if err != nil {
		t.Fatalf("GET session: %v", err)
	}
	defer get.Body.Close()
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", get.StatusCode)
	}
	var sess struct {
		ID     string `json:"id"`
		Prompt string `json:"prompt"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(get.Body).Decode(&sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if sess.ID != out.SessionID || sess.Prompt != "一家咖啡店的电商网站" || sess.Status != "building" {
		t.Fatalf("unexpected session: %+v", sess)
	}

	// The pipeline runs asynchronously; wait for it so the workspace
	// cleanup cannot race in-flight writes.
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+out.SessionID+"/events", 0, 40)
}
