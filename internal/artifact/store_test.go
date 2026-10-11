package artifact

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreTreeLifecycle(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "index.html"), "<html>v1</html>")
	writeTestFile(t, filepath.Join(dir, "assets", "app.js"), "console.log('v1')")
	store := New(NewMemoryBackend())
	ctx := context.Background()

	firstID, err := store.PutTree(ctx, "sessions/abc", dir)
	if err != nil {
		t.Fatalf("PutTree: %v", err)
	}
	assertBuildContent(t, store, "sessions/abc", "index.html", firstID, "<html>v1</html>")
	assertBuildContent(t, store, "sessions/abc", "assets/app.js", firstID, "console.log('v1')")

	writeTestFile(t, filepath.Join(dir, "index.html"), "<html>v2</html>")
	secondID, err := store.PutTree(ctx, "sessions/abc", dir)
	if err != nil {
		t.Fatalf("second PutTree: %v", err)
	}
	if secondID == firstID {
		t.Fatalf("second build id = first %q", firstID)
	}
	assertBuildContent(t, store, "sessions/abc", "index.html", secondID, "<html>v2</html>")
}

func TestStageTreeDoesNotChangeCurrentUntilActivated(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "index.html"), "old")
	store := New(NewMemoryBackend())
	ctx := context.Background()
	oldID, err := store.PutTree(ctx, "sessions/abc", dir)
	if err != nil {
		t.Fatalf("PutTree: %v", err)
	}

	writeTestFile(t, filepath.Join(dir, "index.html"), "new")
	newID, err := store.StageTree(ctx, "sessions/abc", dir)
	if err != nil {
		t.Fatalf("StageTree: %v", err)
	}
	assertBuildContent(t, store, "sessions/abc", "index.html", oldID, "old")
	if err := store.Activate(ctx, "sessions/abc", newID); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	assertBuildContent(t, store, "sessions/abc", "index.html", newID, "new")
}

func TestCopyTreeAndDeletePrefix(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "index.html"), "winner")
	writeTestFile(t, filepath.Join(dir, "main.js"), "VariantB")
	store := New(NewMemoryBackend())
	ctx := context.Background()
	if _, err := store.PutTree(ctx, "sessions/abc/contest/contestant-b", dir); err != nil {
		t.Fatalf("PutTree: %v", err)
	}
	copiedID, err := store.CopyTree(ctx, "sessions/abc/contest/contestant-b", "sessions/abc")
	if err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	assertBuildContent(t, store, "sessions/abc", "main.js", copiedID, "VariantB")

	if err := store.DeletePrefix(ctx, "sessions/abc"); err != nil {
		t.Fatalf("DeletePrefix: %v", err)
	}
	_, _, err = store.Open(ctx, "sessions/abc", "index.html")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open after delete err = %v, want ErrNotFound", err)
	}
}

func TestClearCurrentPreservesContestArchives(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "index.html"), "official")
	store := New(NewMemoryBackend())
	ctx := context.Background()
	if _, err := store.PutTree(ctx, "sessions/abc", dir); err != nil {
		t.Fatalf("PutTree official: %v", err)
	}
	if _, err := store.PutTree(ctx, "sessions/abc/contest/contestant-a", dir); err != nil {
		t.Fatalf("PutTree contest: %v", err)
	}
	if err := store.ClearCurrent(ctx, "sessions/abc"); err != nil {
		t.Fatalf("ClearCurrent: %v", err)
	}
	if _, _, err := store.Open(ctx, "sessions/abc", "index.html"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("official Open after clear err = %v, want ErrNotFound", err)
	}
	obj, _, err := store.Open(ctx, "sessions/abc/contest/contestant-a", "index.html")
	if err != nil {
		t.Fatalf("contest Open after clear: %v", err)
	}
	defer obj.Body.Close()
	body, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatalf("read contest archive: %v", err)
	}
	if strings.TrimSpace(string(body)) != "official" {
		t.Fatalf("contest archive = %q, want official", string(body))
	}
}

func TestStoreRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "index.html"), "ok")
	store := New(NewMemoryBackend())
	if _, err := store.PutTree(context.Background(), "../escape", dir); err == nil {
		t.Fatal("PutTree accepted traversal prefix")
	}
	if _, err := store.PutTree(context.Background(), "sessions/abc", dir); err != nil {
		t.Fatalf("PutTree: %v", err)
	}
	if _, _, err := store.Open(context.Background(), "sessions/abc", "../secret"); err == nil {
		t.Fatal("Open accepted traversal path")
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertBuildContent(t *testing.T, store *Store, prefix, rel, wantID, want string) {
	t.Helper()
	obj, id, err := store.Open(context.Background(), prefix, rel)
	if err != nil {
		t.Fatalf("Open %s/%s: %v", prefix, rel, err)
	}
	defer obj.Body.Close()
	body, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatalf("read %s/%s: %v", prefix, rel, err)
	}
	if id != wantID {
		t.Fatalf("build id = %q, want %q", id, wantID)
	}
	if strings.TrimSpace(string(body)) != want {
		t.Fatalf("content = %q, want %q", string(body), want)
	}
}
