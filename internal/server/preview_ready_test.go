package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
)

// gatedProvider blocks inside Generate until released, so a session can be
// held in 构建中 deterministically instead of racing the Worker.
type gatedProvider struct{ release chan struct{} }

func (gatedProvider) Name() string { return "gated" }

func (g gatedProvider) Generate(ctx context.Context, prompt string, emit func(stage, detail string)) (provider.FileSet, error) {
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return provider.FakeProvider{}.Generate(ctx, prompt, emit)
}

// previewReadyOf reads the derived flag from the single-session response.
func previewReadyOf(t *testing.T, tsURL, id string) bool {
	t.Helper()
	resp, err := http.Get(tsURL + "/api/sessions/" + id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get session status = %d, want 200", resp.StatusCode)
	}
	var sess struct {
		PreviewReady bool `json:"previewReady"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return sess.PreviewReady
}

// TestPreviewReadyReflectsServableBuild pins the flag to what /app can
// actually serve: false while 构建中, true once a build landed, and false
// again when the build output disappears. The client relies on this instead
// of loading an error page and falling back after the fact (ADR-0014).
func TestPreviewReadyReflectsServableBuild(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	// Never let a failing assertion strand the blocked Worker: Server.Close
	// waits for its goroutines.
	t.Cleanup(release)

	ts, dir := newTestServerDir(t, gatedProvider{release: gate})
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")

	// 构建中：还没有可服务的产物，列表与详情都必须报 false。
	if s := findSummary(t, listSessions(t, ts.URL, ""), id); s.PreviewReady {
		t.Fatal("building session reported previewReady = true, want false")
	}
	if previewReadyOf(t, ts.URL, id) {
		t.Fatal("building session detail reported previewReady = true, want false")
	}

	// 放行到完成：产物落盘，两处都变成 true。
	release()
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)
	if s := findSummary(t, listSessions(t, ts.URL, ""), id); !s.PreviewReady {
		t.Fatal("ready session reported previewReady = false, want true")
	}
	if !previewReadyOf(t, ts.URL, id) {
		t.Fatal("ready session detail reported previewReady = false, want true")
	}

	// 就绪但产物缺失（例如被清理）：必须回到 false，而不是让前端自己撞上错误页。
	dists, err := filepath.Glob(filepath.Join(dir, "*", "*", "dist"))
	if err != nil || len(dists) != 1 {
		t.Fatalf("expected exactly one dist under the workspace, got %v (err=%v)", dists, err)
	}
	if err := os.RemoveAll(dists[0]); err != nil {
		t.Fatalf("remove dist: %v", err)
	}
	if s := findSummary(t, listSessions(t, ts.URL, ""), id); s.PreviewReady {
		t.Fatal("session without a build reported previewReady = true, want false")
	}
	if previewReadyOf(t, ts.URL, id) {
		t.Fatal("session without a build detail reported previewReady = true, want false")
	}
}
