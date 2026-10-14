package artifact

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stage writes a build with one file and returns its id.
func stage(t *testing.T, store *Store, prefix, marker string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(marker), 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	id, err := store.StageTree(context.Background(), prefix, dir)
	if err != nil {
		t.Fatalf("stage build: %v", err)
	}
	return id
}

// TestCollectNeverRemovesTheActiveBuild pins the one hard rule of the policy.
// The pointer is what /app and the preview read, so removing its build turns
// both into a 404 — no count or age limit may override that.
func TestCollectNeverRemovesTheActiveBuild(t *testing.T) {
	ctx := context.Background()
	store := New(NewMemoryBackend())
	prefix := SessionPrefix("sess")

	// Activate the OLDEST build, then stage several newer ones.
	oldest := stage(t, store, prefix, "oldest")
	if err := store.Activate(ctx, prefix, oldest); err != nil {
		t.Fatalf("activate: %v", err)
	}
	for i := 0; i < 4; i++ {
		stage(t, store, prefix, "newer")
	}

	// Keep only the two newest: the active build is well outside that window.
	out, err := store.Collect(ctx, prefix, RetentionPolicy{Keep: 2})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if out.Active != oldest {
		t.Fatalf("reported active = %q, want %q", out.Active, oldest)
	}
	active, ok, err := store.Current(ctx, prefix)
	if err != nil || !ok || active != oldest {
		t.Fatalf("active build after collect = %q ok=%v err=%v, want %q", active, ok, err, oldest)
	}
	if _, _, err := store.Open(ctx, prefix, "index.html"); err != nil {
		t.Fatalf("the active build is no longer readable: %v", err)
	}
}

// TestCollectDropsByCountAndByAge covers the two limits separately, and the
// fact that they are ANDed: a build must satisfy both to survive.
func TestCollectDropsByCountAndByAge(t *testing.T) {
	ctx := context.Background()

	t.Run("count", func(t *testing.T) {
		store := New(NewMemoryBackend())
		prefix := SessionPrefix("sess")
		for i := 0; i < 5; i++ {
			stage(t, store, prefix, "build")
		}
		out, err := store.Collect(ctx, prefix, RetentionPolicy{Keep: 2})
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		if len(out.Kept) != 2 || len(out.Removed) != 3 {
			t.Fatalf("kept=%d removed=%d, want 2 and 3", len(out.Kept), len(out.Removed))
		}
		builds, err := store.Builds(ctx, prefix)
		if err != nil {
			t.Fatalf("builds: %v", err)
		}
		if len(builds) != 2 {
			t.Fatalf("builds after collect = %d, want 2", len(builds))
		}
	})

	t.Run("age", func(t *testing.T) {
		store := New(NewMemoryBackend())
		prefix := SessionPrefix("sess")
		var ids []string
		for i := 0; i < 3; i++ {
			ids = append(ids, stage(t, store, prefix, "build"))
		}
		if err := store.Activate(ctx, prefix, ids[1]); err != nil {
			t.Fatalf("activate: %v", err)
		}
		// A window narrower than the time it took to stage the builds: every
		// build is already older than it, so all but the active one go.
		out, err := store.Collect(ctx, prefix, RetentionPolicy{Keep: 50, MaxAge: time.Nanosecond})
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		if len(out.Removed) != 2 {
			t.Fatalf("removed = %d, want 2 (the age limit alone must drop builds)", len(out.Removed))
		}
		if out.Active != ids[1] {
			t.Fatalf("active = %q, want %q", out.Active, ids[1])
		}

		// The same builds survive a window wide enough to hold them.
		store2 := New(NewMemoryBackend())
		prefix2 := SessionPrefix("sess")
		for i := 0; i < 3; i++ {
			stage(t, store2, prefix2, "build")
		}
		out2, err := store2.Collect(ctx, prefix2, RetentionPolicy{Keep: 50, MaxAge: time.Hour})
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		if len(out2.Removed) != 0 {
			t.Fatalf("removed = %d, want 0 within the window", len(out2.Removed))
		}
	})
}

// TestCollectSessionDropsPublishedCopies covers the accumulation that motivated
// the policy: every publish copies the current build into the published prefix,
// so repeated publishing leaves one full copy per publish behind.
func TestCollectSessionDropsPublishedCopies(t *testing.T) {
	ctx := context.Background()
	store := New(NewMemoryBackend())
	id := "sess"
	prefix := SessionPrefix(id)

	draft := stage(t, store, prefix, "draft")
	if err := store.Activate(ctx, prefix, draft); err != nil {
		t.Fatalf("activate: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.CopyTree(ctx, prefix, PublishedPrefix(id)); err != nil {
			t.Fatalf("copy to published: %v", err)
		}
	}
	// A contest archive and its own history must be walked too.
	contest := ContestPrefix(id, "gemma")
	for i := 0; i < 4; i++ {
		stage(t, store, contest, "contest")
	}

	collections, err := store.CollectSession(ctx, id, RetentionPolicy{Keep: 2})
	if err != nil {
		t.Fatalf("collect session: %v", err)
	}
	if len(collections) < 3 {
		t.Fatalf("collections = %d, want the draft, published and contest prefixes", len(collections))
	}

	published, err := store.Builds(ctx, PublishedPrefix(id))
	if err != nil {
		t.Fatalf("published builds: %v", err)
	}
	if len(published) != 1 {
		t.Fatalf("published builds = %d, want 1 (only the active copy)", len(published))
	}
	if _, _, err := store.Open(ctx, PublishedPrefix(id), "index.html"); err != nil {
		t.Fatalf("the published snapshot is no longer readable: %v", err)
	}

	archived, err := store.Builds(ctx, contest)
	if err != nil {
		t.Fatalf("contest builds: %v", err)
	}
	if len(archived) != 2 {
		t.Fatalf("contest builds = %d, want 2", len(archived))
	}
}

// TestSessionIDsEnumeratesTheStore covers the walk a sweep needs: the ids come
// from the object namespace, not from the session database.
func TestSessionIDsEnumeratesTheStore(t *testing.T) {
	ctx := context.Background()
	store := New(NewMemoryBackend())
	stage(t, store, SessionPrefix("alpha"), "a")
	stage(t, store, SessionPrefix("beta"), "b")
	stage(t, store, PublishedPrefix("alpha"), "a")

	ids, err := store.SessionIDs(ctx)
	if err != nil {
		t.Fatalf("session ids: %v", err)
	}
	if len(ids) != 2 || ids[0] != "alpha" || ids[1] != "beta" {
		t.Fatalf("session ids = %v, want [alpha beta]", ids)
	}
}
