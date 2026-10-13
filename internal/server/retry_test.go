package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/pipeline"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
)

// slowFailingProvider fails only after delay, so a retry can be observed
// while it is still running.
type slowFailingProvider struct{ delay time.Duration }

func (slowFailingProvider) Name() string { return "slow-failing" }

func (p slowFailingProvider) Generate(ctx context.Context, _ string, _ func(string, string)) (provider.FileSet, error) {
	select {
	case <-time.After(p.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, errors.New("generation failed")
}

// newSlowFailingServer builds a server whose only 生成引擎 always fails, so
// every turn fails at the 生成 stage.
func newSlowFailingServer(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := server.New(server.Config{
		WorkspaceDir: t.TempDir(),
		DevAuth:      true,
		Prov:         slowFailingProvider{delay: delay},
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	return ts
}

func sessionStatus(t *testing.T, tsURL, id string) string {
	t.Helper()
	resp, err := http.Get(tsURL + "/api/sessions/" + id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	defer resp.Body.Close()
	var sess struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return sess.Status
}

func post(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestFailedTurnReportsStageAndHint pins the error event to a stage and an
// actionable hint: the raw provider error alone leaves the user with nothing
// to do next (ticket #30).
func TestFailedTurnReportsStageAndHint(t *testing.T) {
	ts := newSlowFailingServer(t, 0)
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	events := readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	var stage, hint, message string
	for _, ev := range events {
		if ev.Type != "error" {
			continue
		}
		stage, _ = ev.Payload["stage"].(string)
		hint, _ = ev.Payload["hint"].(string)
		message, _ = ev.Payload["message"].(string)
	}
	if stage != pipeline.StageGenerate {
		t.Fatalf("error stage = %q, want %q", stage, pipeline.StageGenerate)
	}
	if hint == "" {
		t.Fatal("error event carries no actionable hint")
	}
	if message == "" {
		t.Fatal("error event carries no message")
	}
	if got := sessionStatus(t, ts.URL, id); got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
}

// TestRetryRerunsTheFailedTurn covers the retry contract: it regenerates from
// the existing requirement and history without appending a message, and it
// cannot start a second pipeline beside a running one.
func TestRetryRerunsTheFailedTurn(t *testing.T) {
	ts := newSlowFailingServer(t, 1200*time.Millisecond)
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	first := readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	if got := sessionStatus(t, ts.URL, id); got != "failed" {
		t.Fatalf("status before retry = %q, want failed", got)
	}
	usersBefore, promptBefore := userMessages(t, ts.URL, id)
	if usersBefore != 1 {
		t.Fatalf("user messages before retry = %d, want 1", usersBefore)
	}

	if code := post(t, ts.URL+"/api/sessions/"+id+"/retry"); code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200", code)
	}
	// The retry is still running (the provider holds the turn for 1.2s), so a
	// second one must be refused rather than racing a second pipeline.
	if code := post(t, ts.URL+"/api/sessions/"+id+"/retry"); code != http.StatusConflict {
		t.Fatalf("retry while building = %d, want 409", code)
	}

	// The retry lands as another turn on the same history.
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", len(first), 40)
	usersAfter, promptAfter := userMessages(t, ts.URL, id)
	if usersAfter != usersBefore {
		t.Fatalf("user messages after retry = %d, want %d (retry must not append)", usersAfter, usersBefore)
	}
	if promptAfter != promptBefore {
		t.Fatalf("prompt changed across retry: %q -> %q", promptBefore, promptAfter)
	}
	if got := sessionStatus(t, ts.URL, id); got != "failed" {
		t.Fatalf("status after retry = %q, want failed", got)
	}
}

// TestRetryRejectsANonFailedTurn keeps retry from becoming a second way to
// regenerate a healthy session.
func TestRetryRejectsANonFailedTurn(t *testing.T) {
	ts := newTestServer(t)
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	if got := sessionStatus(t, ts.URL, id); got != "ready" {
		t.Fatalf("status = %q, want ready", got)
	}
	if code := post(t, ts.URL+"/api/sessions/"+id+"/retry"); code != http.StatusConflict {
		t.Fatalf("retry on a ready session = %d, want 409", code)
	}
	// The rejected attempt must not leave the session claimed as building.
	if got := sessionStatus(t, ts.URL, id); got != "ready" {
		t.Fatalf("status after rejected retry = %q, want ready", got)
	}
}

// userMessages returns the number of user messages and the session prompt.
func userMessages(t *testing.T, tsURL, id string) (int, string) {
	t.Helper()
	resp, err := http.Get(tsURL + "/api/sessions/" + id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	defer resp.Body.Close()
	var sess struct {
		Prompt   string `json:"prompt"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	users := 0
	for _, m := range sess.Messages {
		if m.Role == "user" {
			users++
		}
	}
	return users, sess.Prompt
}
