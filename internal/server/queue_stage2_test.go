package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/jobqueue"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

// gatedQueue wraps a MemoryQueue so a test can flip the backend to "full" on
// demand. This exercises the ADR-0007 contract — a full Queue is a hard 503,
// never a silent drop or fallback to a bare goroutine — without standing up
// Redis or racing a real worker to saturation.
type gatedQueue struct {
	inner *jobqueue.MemoryQueue
	full  atomic.Bool
}

func (q *gatedQueue) Enqueue(ctx context.Context, job jobqueue.Job) error {
	if q.full.Load() {
		return jobqueue.ErrQueueFull
	}
	return q.inner.Enqueue(ctx, job)
}

func (q *gatedQueue) Run(ctx context.Context, workerID string, handle jobqueue.Handler) error {
	return q.inner.Run(ctx, workerID, handle)
}

func (q *gatedQueue) Close() error { return q.inner.Close() }

// newTestServerWithQueue wires a specific Queue into the server and closes it
// (stopping the Worker pool) at test cleanup.
func newTestServerWithQueue(t *testing.T, prov provider.Provider, q jobqueue.Queue) *httptest.Server {
	t.Helper()
	srv := server.New(server.Config{
		WorkspaceDir: t.TempDir(),
		DevAuth:      true,
		Prov:         prov,
		Queue:        q,
		QueueWorkers: 1,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	return ts
}

func getSession(t *testing.T, tsURL, id string) session.Session {
	t.Helper()
	resp, err := http.Get(tsURL + "/api/sessions/" + id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get session status = %d, want 200", resp.StatusCode)
	}
	var sess session.Session
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return sess
}

// TestQueueFullCreateReturns503AndMarksFailed: when the Queue is full, the
// create turn must surface 503 and leave the session in "failed" rather than
// a stuck "building" or a silently dropped generation.
func TestQueueFullCreateReturns503AndMarksFailed(t *testing.T) {
	q := &gatedQueue{inner: jobqueue.NewMemoryQueue(1)}
	q.full.Store(true)
	ts := newTestServerWithQueue(t, provider.NewTemplate(), q)

	resp, err := http.Post(ts.URL+"/api/sessions", "application/json",
		strings.NewReader(`{"prompt":"一家咖啡店的电商网站"}`))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("create status = %d, want 503", resp.StatusCode)
	}

	sessions := listSessions(t, ts.URL, "")
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	if sessions[0].Status != "failed" {
		t.Fatalf("status = %q, want failed", sessions[0].Status)
	}
}

// TestQueueFullChatReturns503AndRollsBack: a chat turn that cannot be enqueued
// must roll the session status back to its prior value so the session is not
// wedged in "building", and a later retry (Queue healthy again) must succeed.
func TestQueueFullChatReturns503AndRollsBack(t *testing.T) {
	q := &gatedQueue{inner: jobqueue.NewMemoryQueue(4)}
	ts := newTestServerWithQueue(t, provider.NewTemplate(), q)

	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)
	first := getSession(t, ts.URL, id)
	if first.Status != "ready" {
		t.Fatalf("after first turn status = %q, want ready", first.Status)
	}

	// Queue goes full: the chat is refused and the status rolls back to ready.
	q.full.Store(true)
	chat, err := http.Post(ts.URL+"/api/sessions/"+id+"/chat", "application/json",
		strings.NewReader(`{"message":"把主题改成蓝色"}`))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	chat.Body.Close()
	if chat.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("chat status = %d, want 503", chat.StatusCode)
	}
	if got := getSession(t, ts.URL, id).Status; got != "ready" {
		t.Fatalf("status after refused chat = %q, want ready (rolled back)", got)
	}

	// Queue recovers: the same session accepts a turn again.
	q.full.Store(false)
	retry, err := http.Post(ts.URL+"/api/sessions/"+id+"/chat", "application/json",
		strings.NewReader(`{"message":"把主题改成蓝色"}`))
	if err != nil {
		t.Fatalf("retry chat: %v", err)
	}
	retry.Body.Close()
	if retry.StatusCode != http.StatusOK {
		t.Fatalf("retry chat status = %d, want 200", retry.StatusCode)
	}
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", len(first.Events), 80)
	if got := getSession(t, ts.URL, id).Status; got != "ready" {
		t.Fatalf("status after retry = %q, want ready", got)
	}
}

// TestStaleJobIsDroppedWithoutSideEffects: a Job delivered for a session that
// is no longer "building" (the turn already finished, or the status was rolled
// back) must be acknowledged with no events and no status change. This is the
// at-least-once redelivery guard behind the HTTP 409.
func TestStaleJobIsDroppedWithoutSideEffects(t *testing.T) {
	q := jobqueue.NewMemoryQueue(4)
	ts := newTestServerWithQueue(t, provider.NewTemplate(), q)

	id := createSession(t, ts.URL, "一个数据看板")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	before := getSession(t, ts.URL, id)
	if before.Status != "ready" {
		t.Fatalf("status = %q, want ready", before.Status)
	}

	// Hand a duplicate Turn Job straight to the Queue after the turn is done.
	if err := q.Enqueue(context.Background(), jobqueue.Job{
		Owner:     "dev-user",
		SessionID: id,
		Kind:      jobqueue.KindTurn,
	}); err != nil {
		t.Fatalf("enqueue stale job: %v", err)
	}

	// Give the Worker time to consume and drop it, then assert nothing moved.
	time.Sleep(300 * time.Millisecond)
	after := getSession(t, ts.URL, id)
	if after.Status != "ready" {
		t.Fatalf("status = %q, want ready (stale job must not rerun)", after.Status)
	}
	if len(after.Events) != len(before.Events) {
		t.Fatalf("events = %d, want %d (stale job produced events)",
			len(after.Events), len(before.Events))
	}
}
