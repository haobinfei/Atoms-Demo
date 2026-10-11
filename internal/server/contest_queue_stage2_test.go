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
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

// fixedScore is a deterministic Nina so the contest tests do not depend on an
// LLM or the heuristic.
func fixedScore(_ context.Context, _ string, _ provider.FileSet) (session.Scores, string, error) {
	return session.Scores{Design: 8, Functionality: 7, CodeQuality: 9}, "点评", nil
}

// newContestServerWithWorkers is newContestServer plus an explicit Worker pool
// size, so a test can prove a single Worker still fans contestants out.
func newContestServerWithWorkers(t *testing.T, competitors []provider.Provider, workers int) *httptest.Server {
	t.Helper()
	srv := server.New(server.Config{
		WorkspaceDir: t.TempDir(),
		DevAuth:      true,
		Prov:         provider.FakeProvider{},
		Competitors:  competitors,
		ScoreFunc:    fixedScore,
		QueueWorkers: workers,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	return ts
}

// TestContestSingleWorkerStillParallel: the whole contest is one Job, but its
// contestants still generate concurrently — a Worker pool of 1 must not
// serialize the two contestants (ADR-0007, issue #19).
func TestContestSingleWorkerStillParallel(t *testing.T) {
	a := &contestStub{name: "contestant-a", marker: "VariantA", delay: 250 * time.Millisecond}
	b := &contestStub{name: "contestant-b", marker: "VariantB", delay: 250 * time.Millisecond}
	ts := newContestServerWithWorkers(t, []provider.Provider{a, b}, 1)
	id := createContest(t, ts.URL, "一个数据看板")

	started := time.Now()
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 60)
	elapsed := time.Since(started)

	aStart, aEnd := a.window()
	bStart, bEnd := b.window()
	if !aStart.Before(bEnd) || !bStart.Before(aEnd) {
		t.Fatalf("contestants did not overlap with worker=1: a=%v..%v b=%v..%v", aStart, aEnd, bStart, bEnd)
	}
	// Two 250ms contestants run in parallel; sequential would be ≥500ms and
	// scoring adds a little, so a generous 3s bound proves no serialization.
	if elapsed > 3*time.Second {
		t.Fatalf("contest took %v; contestants were serialized", elapsed)
	}
}

// TestContestChatWhileBuildingIs409: a contest keeps the session "building"
// until it finishes, so a chat request mid-contest is a 409.
func TestContestChatWhileBuildingIs409(t *testing.T) {
	a := &contestStub{name: "contestant-a", marker: "VariantA", delay: 500 * time.Millisecond}
	ts := newContestServerWithWorkers(t, []provider.Provider{a}, 1)
	id := createContest(t, ts.URL, "一个数据看板")

	resp, err := http.Post(ts.URL+"/api/sessions/"+id+"/chat", "application/json",
		strings.NewReader(`{"message":"把主题改成蓝色"}`))
	if err != nil {
		t.Fatalf("chat during contest: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("chat during contest status = %d, want 409", resp.StatusCode)
	}

	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 60)
}

// TestContestQueueFullCreateReturns503: a full Queue refuses the contest Job
// with 503 and leaves the session "failed", never a silent drop.
func TestContestQueueFullCreateReturns503(t *testing.T) {
	q := &gatedQueue{inner: jobqueue.NewMemoryQueue(1)}
	q.full.Store(true)
	ts := newTestServerWithQueue(t, provider.NewTemplate(), q)

	resp, err := http.Post(ts.URL+"/api/sessions", "application/json",
		strings.NewReader(`{"prompt":"一个数据看板","competition":true}`))
	if err != nil {
		t.Fatalf("create contest: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("create contest status = %d, want 503", resp.StatusCode)
	}

	sessions := listSessions(t, ts.URL, "")
	if len(sessions) != 1 || sessions[0].Status != "failed" {
		t.Fatalf("sessions = %+v, want one failed", sessions)
	}
}

// TestStaleContestJobIsDropped: a contest Job delivered after the session is
// no longer "building" is acknowledged with no events and no rerun.
func TestStaleContestJobIsDropped(t *testing.T) {
	a := &contestStub{name: "contestant-a", marker: "VariantA", delay: 50 * time.Millisecond}
	q := jobqueue.NewMemoryQueue(4)
	srv := server.New(server.Config{
		WorkspaceDir: t.TempDir(),
		DevAuth:      true,
		Prov:         provider.FakeProvider{},
		Competitors:  []provider.Provider{a},
		ScoreFunc:    fixedScore,
		Queue:        q,
		QueueWorkers: 1,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	id := createContest(t, ts.URL, "一个数据看板")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 60)

	before := getSession(t, ts.URL, id)
	if before.Status != "ready" {
		t.Fatalf("status = %q, want ready", before.Status)
	}

	if err := q.Enqueue(context.Background(), jobqueue.Job{
		Owner:     "dev-user",
		SessionID: id,
		Kind:      jobqueue.KindContest,
	}); err != nil {
		t.Fatalf("enqueue stale contest job: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	after := getSession(t, ts.URL, id)
	if after.Status != "ready" {
		t.Fatalf("status = %q, want ready (stale contest must not rerun)", after.Status)
	}
	if len(after.Events) != len(before.Events) {
		t.Fatalf("events = %d, want %d (stale contest produced events)",
			len(after.Events), len(before.Events))
	}
}
