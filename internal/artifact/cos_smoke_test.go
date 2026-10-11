package artifact

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCOSSmoke exercises the real Tencent COS S3-compatible backend. It is
// opt-in because it needs bucket credentials and network access; regular test
// runs stay hermetic.
func TestCOSSmoke(t *testing.T) {
	if os.Getenv("COS_SMOKE") != "1" {
		t.Skip("set COS_SMOKE=1 with COS_* credentials to run")
	}
	store, err := NewCOS(COSConfig{
		Bucket:       os.Getenv("COS_BUCKET"),
		Endpoint:     os.Getenv("COS_ENDPOINT"),
		SecretID:     os.Getenv("COS_SECRET_ID"),
		SecretKey:    os.Getenv("COS_SECRET_KEY"),
		SessionToken: os.Getenv("COS_SESSION_TOKEN"),
	})
	if err != nil {
		t.Fatalf("NewCOS: %v", err)
	}

	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "index.html"), "<html><body>cos-smoke</body></html>")
	writeTestFile(t, filepath.Join(dir, "assets", "app.js"), "console.log('cos-smoke')")
	prefix := fmt.Sprintf("_smoke/%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := store.DeletePrefix(ctx, prefix); err != nil {
			t.Errorf("cleanup COS smoke prefix: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	buildID, err := store.PutTree(ctx, prefix, dir)
	if err != nil {
		t.Fatalf("PutTree: %v", err)
	}
	if got, ok, err := store.Current(ctx, prefix); err != nil || !ok || got != buildID {
		t.Fatalf("Current: id=%q ok=%v err=%v, want %q", got, ok, err, buildID)
	}
	obj, gotID, err := store.Open(ctx, prefix, "index.html")
	if err != nil {
		t.Fatalf("Open index: %v", err)
	}
	defer obj.Body.Close()
	body, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if gotID != buildID || !strings.Contains(string(body), "cos-smoke") {
		t.Fatalf("Open returned id=%q body=%q, want id=%q", gotID, string(body), buildID)
	}
	if !strings.HasPrefix(obj.ContentType, "text/html") {
		t.Fatalf("index content type = %q, want text/html", obj.ContentType)
	}
}
