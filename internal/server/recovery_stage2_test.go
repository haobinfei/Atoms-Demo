package server_test

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/jobqueue"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

// waitForStatus polls the shared store until the session leaves "building"
// (or reaches want), so a test can observe an asynchronously recovered run.
func waitForStatus(t *testing.T, store session.Store, owner, id, want string) session.Session {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last session.Session
	for time.Now().Before(deadline) {
		sess, err := store.Load(owner, id)
		if err == nil {
			last = *sess
			if sess.Status == want {
				return *sess
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %s status = %q, want %q within deadline", id, last.Status, want)
	return last
}

func hasEvent(sess session.Session, typ string) bool {
	for _, ev := range sess.Events {
		if ev.Type == typ {
			return true
		}
	}
	return false
}

func countEvents(sess session.Session, typ string) int {
	n := 0
	for _, ev := range sess.Events {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// expireLease turns a freshly-created building session into the state left by
// a crashed replica: still building, but with no live execution lease.
func expireLease(t *testing.T, store session.Store, owner, id string) {
	t.Helper()
	sess, err := store.Load(owner, id)
	if err != nil {
		t.Fatalf("load session %s: %v", id, err)
	}
	sess.LeaseOwner = ""
	sess.LeaseUntil = time.Time{}
	if err := store.Save(owner, sess); err != nil {
		t.Fatalf("expire lease for %s: %v", id, err)
	}
}

// TestRecoveryRerunsBuildingSessions is the #21 acceptance test: a fresh
// instance scans building sessions with no live lease, emits a 恢复事件
// (recovered event) for each, and re-runs the crashed turn / contest /
// publish to completion — while a finished session is left untouched.
func TestRecoveryRerunsBuildingSessions(t *testing.T) {
	dir := t.TempDir()
	store := session.NewStore(dir)
	owner := "dev-user"

	// Seed one real build so the publish session has an app + dist to
	// rebuild, then shut the seed instance down (a "crash").
	seed := server.New(server.Config{
		WorkspaceDir: dir, Store: store, DevAuth: true,
		Prov: provider.NewTemplate(), QueueWorkers: 2,
	})
	seedTS := httptest.NewServer(seed.Handler())
	pubID := createSession(t, seedTS.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, seedTS.URL+"/api/sessions/"+pubID+"/events", 0, 40)
	seed.Close()
	seedTS.Close()

	// Preset the crashed sessions: a turn, a contest and a publish, each
	// "building" with a recorded kind but no live lease. Plus a finished
	// session that recovery must not touch.
	turn, err := store.Create(owner, "一个博客")
	if err != nil {
		t.Fatalf("create turn: %v", err)
	}
	if err := store.SetJobKind(owner, turn.ID, "turn"); err != nil {
		t.Fatalf("set turn kind: %v", err)
	}
	expireLease(t, store, owner, turn.ID)
	contest, err := store.Create(owner, "一个数据看板")
	if err != nil {
		t.Fatalf("create contest: %v", err)
	}
	if err := store.SetJobKind(owner, contest.ID, "contest"); err != nil {
		t.Fatalf("set contest kind: %v", err)
	}
	expireLease(t, store, owner, contest.ID)
	// Flip the seeded session back to a crashed publish.
	if err := store.SetStatus(owner, pubID, "building"); err != nil {
		t.Fatalf("set publish building: %v", err)
	}
	if err := store.SetJobKind(owner, pubID, "publish"); err != nil {
		t.Fatalf("set publish kind: %v", err)
	}
	ready, err := store.Create(owner, "一个已经完成的会话")
	if err != nil {
		t.Fatalf("create ready: %v", err)
	}
	if err := store.SetStatus(owner, ready.ID, "ready"); err != nil {
		t.Fatalf("set ready: %v", err)
	}

	// New instance: the startup sweeper must recover all three.
	srv := server.New(server.Config{
		WorkspaceDir: dir, Store: store, DevAuth: true,
		Prov:         provider.NewTemplate(),
		Competitors:  []provider.Provider{provider.FakeProvider{}},
		ScoreFunc:    fixedScore,
		QueueWorkers: 3,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})

	gotTurn := waitForStatus(t, store, owner, turn.ID, "ready")
	if !hasEvent(gotTurn, "recovered") {
		t.Fatalf("recovered turn missing 恢复事件: %+v", gotTurn.Events)
	}

	gotContest := waitForStatus(t, store, owner, contest.ID, "ready")
	if !hasEvent(gotContest, "recovered") {
		t.Fatalf("recovered contest missing 恢复事件: %+v", gotContest.Events)
	}
	if gotContest.Contest == nil || !gotContest.Contest.Done {
		t.Fatalf("recovered contest = %+v, want a finished (Done) contest", gotContest.Contest)
	}

	gotPub := waitForStatus(t, store, owner, pubID, "ready")
	if !hasEvent(gotPub, "recovered") {
		t.Fatalf("recovered publish missing 恢复事件: %+v", gotPub.Events)
	}
	if len(gotPub.Publishes) == 0 {
		t.Fatalf("recovered publish recorded no version: %+v", gotPub.Publishes)
	}

	// The finished session is untouched: still ready, no recovery event.
	gotReady := waitForStatus(t, store, owner, ready.ID, "ready")
	if hasEvent(gotReady, "recovered") || len(gotReady.Events) != 0 {
		t.Fatalf("non-building session was modified: %+v", gotReady.Events)
	}
}

// countingProvider wraps a real provider and counts Generate calls, so a test
// can prove a session is executed exactly once even when two replicas sweep it.
type countingProvider struct {
	inner provider.Provider
	calls int32
}

func (c *countingProvider) Name() string { return c.inner.Name() }

func (c *countingProvider) Generate(ctx context.Context, prompt string, emit func(string, string)) (provider.FileSet, error) {
	atomic.AddInt32(&c.calls, 1)
	time.Sleep(150 * time.Millisecond) // widen the double-run window
	return c.inner.Generate(ctx, prompt, emit)
}

// TestRecoveryClaimPreventsDoubleRun: two replicas sharing one store both run
// the sweeper, but the 认领租约 (execution lease) lets exactly one of them run
// the recovered session — no duplicate concurrent execution (#21).
func TestRecoveryClaimPreventsDoubleRun(t *testing.T) {
	dir := t.TempDir()
	store := session.NewStore(dir)
	owner := "dev-user"

	sess, err := store.Create(owner, "一个博客")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.SetJobKind(owner, sess.ID, "turn"); err != nil {
		t.Fatalf("set kind: %v", err)
	}
	expireLease(t, store, owner, sess.ID)

	counter := &countingProvider{inner: provider.NewTemplate()}
	cfg := server.Config{
		WorkspaceDir: dir, Store: store, DevAuth: true,
		Prov: counter, QueueWorkers: 2,
	}
	replicas := make([]*server.Server, 0, 2)
	for i := 0; i < 2; i++ {
		s := server.New(cfg)
		ts := httptest.NewServer(s.Handler())
		t.Cleanup(func() {
			ts.Close()
			_ = s.Close()
		})
		replicas = append(replicas, s)
	}

	got := waitForStatus(t, store, owner, sess.ID, "ready")
	if n := atomic.LoadInt32(&counter.calls); n != 1 {
		t.Fatalf("Generate calls = %d, want exactly 1 (lease must prevent a double run)", n)
	}
	if n := countEvents(got, "done"); n != 1 {
		t.Fatalf("done events = %d, want 1: %+v", n, got.Events)
	}
	if n := countEvents(got, "recovered"); n != 1 {
		t.Fatalf("recovered events = %d, want 1 (announced once): %+v", n, got.Events)
	}
}

// TestStaleJobKindIsDropped protects the session from a redelivered Job from
// an older task type: only the kind currently recorded as building may run.
func TestStaleJobKindIsDropped(t *testing.T) {
	dir := t.TempDir()
	store := session.NewStore(dir)
	owner := "dev-user"

	sess, err := store.Create(owner, "一个博客")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.SetJobKind(owner, sess.ID, string(jobqueue.KindContest)); err != nil {
		t.Fatalf("set kind: %v", err)
	}

	q := jobqueue.NewMemoryQueue(4)
	srv := server.New(server.Config{
		WorkspaceDir: dir, Store: store, DevAuth: true,
		Prov: provider.NewTemplate(), Queue: q, QueueWorkers: 1,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})

	if err := q.Enqueue(context.Background(), jobqueue.Job{
		Owner: owner, SessionID: sess.ID, Kind: jobqueue.KindTurn,
	}); err != nil {
		t.Fatalf("enqueue stale turn: %v", err)
	}
	time.Sleep(250 * time.Millisecond)

	got, err := store.Load(owner, sess.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Status != "building" || len(got.Events) != 0 {
		t.Fatalf("stale turn ran: status=%q events=%+v", got.Status, got.Events)
	}
}
