package session_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/session"
)

func TestValidOwner(t *testing.T) {
	valid := []string{"user-a", "dev-user", "alice@example.com", "a.b_c-d"}
	for _, name := range valid {
		if !session.ValidOwner(name) {
			t.Fatalf("ValidOwner(%q) = false, want true", name)
		}
	}
	long := string(make([]byte, 200))
	invalid := []string{"", "..", "../etc", "a/b", `a\b`, "a b", "中文", long}
	for _, name := range invalid {
		if session.ValidOwner(name) {
			t.Fatalf("ValidOwner(%q) = true, want false", name)
		}
	}
}

// TestIndexOwnerRejectsTampering ensures a forged by-id entry can never
// steer the public /app handler outside the workspace.
func TestIndexOwnerRejectsTampering(t *testing.T) {
	dir := t.TempDir()
	store := session.NewStore(dir)
	sess, err := store.Create("user-a", "一个数据看板")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	owner, err := store.IndexOwner(sess.ID)
	if err != nil || owner != "user-a" {
		t.Fatalf("IndexOwner = %q, %v; want user-a", owner, err)
	}

	index := filepath.Join(dir, "by-id", sess.ID)
	for _, forged := range []string{"../user-a", "user-a/../../etc", ""} {
		if err := os.WriteFile(index, []byte(forged), 0o644); err != nil {
			t.Fatalf("forge index: %v", err)
		}
		if _, err := store.IndexOwner(sess.ID); err == nil {
			t.Fatalf("IndexOwner accepted forged owner %q", forged)
		}
	}

	if _, err := store.IndexOwner("../" + sess.ID); err == nil {
		t.Fatal("IndexOwner accepted traversal id")
	}
	if _, err := store.IndexOwner("missing"); err == nil {
		t.Fatal("IndexOwner accepted unknown id")
	}
}

func TestCreateRejectsInvalidOwner(t *testing.T) {
	store := session.NewStore(t.TempDir())
	if _, err := store.Create("../evil", "prompt"); err == nil {
		t.Fatal("Create accepted an invalid owner")
	}
}

// Ticket #57: a turn's wait is the gap between being queued and being claimed.
// Create and BeginTurn start the clock; ClaimLease is what stops it.
func TestQueuedAndStartedTimestamps(t *testing.T) {
	store := session.NewStore(t.TempDir())

	// A new session is born queued, with nothing started yet.
	sess, err := store.Create("user-a", "一个数据看板")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sess.QueuedAt == nil {
		t.Fatal("Create did not record queuedAt")
	}
	if sess.StartedAt != nil {
		t.Fatal("Create recorded startedAt before any worker claimed the session")
	}

	// A worker claiming the lease is what marks the turn as started.
	ok, err := store.ClaimLease("user-a", sess.ID, "worker-1", time.Minute)
	if err != nil || !ok {
		t.Fatalf("ClaimLease = %v, %v; want true", ok, err)
	}
	loaded, err := store.Load("user-a", sess.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.StartedAt == nil {
		t.Fatal("ClaimLease did not record startedAt")
	}
	if !loaded.StartedAt.After(*loaded.QueuedAt) && !loaded.StartedAt.Equal(*loaded.QueuedAt) {
		t.Fatalf("startedAt %v is before queuedAt %v", loaded.StartedAt, loaded.QueuedAt)
	}

	// A follow-up turn restarts the clock: new queuedAt, no startedAt until a
	// worker claims it again.
	if err := store.SetStatus("user-a", sess.ID, "ready"); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if _, ok, err := store.BeginTurn("user-a", sess.ID, "turn"); err != nil || !ok {
		t.Fatalf("BeginTurn = %v, %v; want true", ok, err)
	}
	next, err := store.Load("user-a", sess.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if next.StartedAt != nil {
		t.Fatal("BeginTurn left the previous turn's startedAt in place")
	}
	if next.QueuedAt == nil || !next.QueuedAt.After(*loaded.QueuedAt) {
		t.Fatalf("BeginTurn did not advance queuedAt: %v", next.QueuedAt)
	}
}
