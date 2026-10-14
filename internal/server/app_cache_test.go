package server_test

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
)

// getWithHeaders fetches url and returns the response plus its body, so the
// caller can assert on headers and content together.
func getWithHeaders(t *testing.T, url string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp, string(raw)
}

// TestAppAssetsSurviveASameSecondRebuild pins the caching contract that the
// preview depends on. HTTP dates have one-second granularity, so a rebuild
// that lands in the same second as the previous one leaves the asset's mtime
// unchanged; a handler that answers If-Modified-Since then returns 304 and the
// browser keeps rendering the pre-rebuild bundle. The test forces that exact
// situation — same mtime before and after a successful rebuild — and requires
// the new bytes.
//
// This is the regression the ticket is about: the save succeeded, the rebuild
// succeeded, and the preview still showed the old app.
func TestAppAssetsSurviveASameSecondRebuild(t *testing.T) {
	ts, dir := newTestServerDir(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	// Find the bundle the built page references, the same way the browser
	// would load it.
	page := getBody(t, ts.URL+"/app/"+id+"/")
	m := regexp.MustCompile(`src="([^"]+\.js)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no bundle referenced by the built page: %s", page)
	}
	assetURL := ts.URL + resolveAppAsset(ts.URL, id, m[1])
	assetPath := filepath.Join(dir, "dev-user", id, "dist", filepath.Base(m[1]))

	// First edit: the bundle now carries MARKER_ONE.
	putFileAndRebuild(t, ts.URL, id, "MARKER_ONE")
	resp, first := getWithHeaders(t, assetURL, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first fetch = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(first, "MARKER_ONE") {
		t.Fatalf("bundle does not carry the first edit: %s", snippet(first))
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}

	// Pin the mtime the way the pre-rebuild file had it: the same second, to
	// the second, as far as any HTTP validator can tell.
	stamp := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(assetPath, stamp, stamp); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// Second edit: the rebuild rewrites the bundle with MARKER_TWO, and the
	// mtime is forced back into the same second as before.
	putFileAndRebuild(t, ts.URL, id, "MARKER_TWO")
	if err := os.Chtimes(assetPath, stamp, stamp); err != nil {
		t.Fatalf("chtimes after rebuild: %v", err)
	}

	// A client holding the previous copy revalidates with the Last-Modified it
	// saw. It must be told the bytes changed, not "use what you have".
	resp, second := getWithHeaders(t, assetURL, map[string]string{
		"If-Modified-Since": stamp.UTC().Format(http.TimeFormat),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("same-second revalidation = %d, want 200 — the stale bundle wins again", resp.StatusCode)
	}
	if !strings.Contains(second, "MARKER_TWO") {
		t.Fatalf("same-second revalidation served stale bytes: %s", snippet(second))
	}
}

// TestPreviewIndexSurvivesASameSecondRebuild covers the entry point: the
// iframe reloads index.html after every save, and a cached index would boot the
// previous bundle even when the assets are fresh.
func TestPreviewIndexSurvivesASameSecondRebuild(t *testing.T) {
	ts, dir := newTestServerDir(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	putFileAndRebuild(t, ts.URL, id, "MARKER_ONE")
	indexPath := filepath.Join(dir, "dev-user", id, "dist", "index.html")
	stamp := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(indexPath, stamp, stamp); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	resp, first := getWithHeaders(t, ts.URL+"/app/"+id+"/", map[string]string{
		"If-Modified-Since": stamp.UTC().Format(http.TimeFormat),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("index revalidation = %d, want 200", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("index Cache-Control = %q, want no-store", cc)
	}
	// The storage-namespace injection must still be in place: the page is not
	// passed through verbatim.
	if !strings.Contains(first, "data-atoms-storage-ns") {
		t.Fatalf("index lost the storage namespace injection: %s", snippet(first))
	}
}

func putFileAndRebuild(t *testing.T, base, id, marker string) {
	t.Helper()
	const edited = "export default function App(){ return <main>" + "HAND_EDIT_%s</main> }"
	status, out := putFile(t, base, id, "src/App.tsx", fmt.Sprintf(edited, marker))
	if status != http.StatusOK {
		t.Fatalf("put status = %d, want 200 (%v)", status, out)
	}
	if out["rebuilt"] != true {
		t.Fatalf("rebuilt = %v, want true (%v)", out["rebuilt"], out)
	}
}

func snippet(s string) string {
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}
