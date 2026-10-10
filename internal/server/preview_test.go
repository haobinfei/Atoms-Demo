package server_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestPreviewServesBuiltApp drives a full session to done, then asserts the
// built app is publicly served at /app/{id}/ without any auth.
func TestPreviewServesBuiltApp(t *testing.T) {
	ts := newTestServer(t)
	id := createSession(t, ts.URL, "一个数据看板")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 40)

	resp, err := http.Get(ts.URL + "/app/" + id + "/")
	if err != nil {
		t.Fatalf("get preview: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `id="root"`) {
		t.Fatal("preview index.html missing root mount point")
	}

	js, err := http.Get(ts.URL + "/app/" + id + "/main.js")
	if err != nil || js.StatusCode != http.StatusOK {
		t.Fatalf("bundle not served: %v status=%v", err, js.StatusCode)
	}
	js.Body.Close()
}

func TestFileListingContentAndExport(t *testing.T) {
	ts := newTestServer(t)
	id := createSession(t, ts.URL, "一个数据看板")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 40)

	// Listing.
	listResp, err := http.Get(ts.URL + "/api/sessions/" + id + "/files")
	if err != nil {
		t.Fatalf("list files: %v", err)
	}
	var listing []struct {
		Path string `json:"path"`
		Size int    `json:"size"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listing); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	listResp.Body.Close()
	if len(listing) == 0 {
		t.Fatal("no files listed")
	}
	var found bool
	for _, f := range listing {
		if f.Path == "src/App.tsx" && f.Size > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("src/App.tsx not in listing: %+v", listing)
	}

	// Content.
	contentResp, err := http.Get(ts.URL + "/api/sessions/" + id + "/file/src/App.tsx")
	if err != nil {
		t.Fatalf("get file: %v", err)
	}
	var file struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(contentResp.Body).Decode(&file); err != nil {
		t.Fatalf("decode file: %v", err)
	}
	contentResp.Body.Close()
	if file.Path != "src/App.tsx" || !strings.Contains(file.Content, "export default") {
		t.Fatalf("unexpected file payload: %+v", file)
	}

	// Traversal must be rejected.
	evil, err := http.Get(ts.URL + "/api/sessions/" + id + "/file/..%2F..%2Fgo.mod")
	if err != nil {
		t.Fatalf("get evil path: %v", err)
	}
	evil.Body.Close()
	if evil.StatusCode != http.StatusBadRequest {
		t.Fatalf("traversal status = %d, want 400", evil.StatusCode)
	}

	// Export zip.
	exportResp, err := http.Get(ts.URL + "/api/sessions/" + id + "/export")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	defer exportResp.Body.Close()
	if ct := exportResp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("export Content-Type = %q", ct)
	}
	body := mustReadAll(t, exportResp.Body)
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("zip decode: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "index.html") || !strings.Contains(joined, "src/App.tsx") {
		t.Fatalf("zip missing files: %v", names)
	}
}

func mustReadAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
