package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/jobqueue"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
)

// holdPublishQueue runs turn/contest Jobs normally but accepts publish Jobs
// without ever delivering them, so a test can observe the session while a
// 发布 (Publish) is claimed but not yet executed.
type holdPublishQueue struct{ inner *jobqueue.MemoryQueue }

func (q *holdPublishQueue) Enqueue(_ context.Context, job jobqueue.Job) error {
	if job.Kind == jobqueue.KindPublish {
		return nil // accepted, deliberately never delivered
	}
	return q.inner.Enqueue(context.Background(), job)
}

func (q *holdPublishQueue) Run(ctx context.Context, id string, h jobqueue.Handler) error {
	return q.inner.Run(ctx, id, h)
}

func (q *holdPublishQueue) Close() error { return q.inner.Close() }

func (q *holdPublishQueue) Stats(ctx context.Context) (jobqueue.Stats, error) {
	return q.inner.Stats(ctx)
}

// TestPublishClaimsSessionWhileQueued: POST /publish returns immediately (202)
// and the session is claimed for the duration, so a chat request mid-publish
// is a 409 and no version is recorded until the Worker runs.
func TestPublishClaimsSessionWhileQueued(t *testing.T) {
	q := &holdPublishQueue{inner: jobqueue.NewMemoryQueue(4)}
	srv := server.New(server.Config{
		WorkspaceDir: t.TempDir(),
		DevAuth:      true,
		Prov:         provider.NewTemplate(),
		Queue:        q,
		QueueWorkers: 1,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})

	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)
	if got := getSession(t, ts.URL, id).Status; got != "ready" {
		t.Fatalf("status after build = %q, want ready", got)
	}

	// Publish is accepted immediately (the rebuild runs on a Worker).
	started := time.Now()
	resp, err := http.Post(ts.URL+"/api/sessions/"+id+"/publish", "application/json", nil)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("publish status = %d, want 202", resp.StatusCode)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("publish blocked for %v; want immediate return", elapsed)
	}

	// The session is claimed: status building, chat 409, no version yet.
	if got := getSession(t, ts.URL, id).Status; got != "building" {
		t.Fatalf("status mid-publish = %q, want building", got)
	}
	chat, err := http.Post(ts.URL+"/api/sessions/"+id+"/chat", "application/json",
		strings.NewReader(`{"message":"把主题改成蓝色"}`))
	if err != nil {
		t.Fatalf("chat mid-publish: %v", err)
	}
	chat.Body.Close()
	if chat.StatusCode != http.StatusConflict {
		t.Fatalf("chat mid-publish status = %d, want 409", chat.StatusCode)
	}
	if n := len(publishHistory(t, ts.URL, id)); n != 0 {
		t.Fatalf("publish history = %d, want 0 (Worker has not run)", n)
	}
}
