package session_test

import (
	"os"
	"path/filepath"
	"testing"

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
