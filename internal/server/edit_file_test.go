package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
)

// blockingProvider keeps a 回合 inside the provider until released, so a test
// can act on a session that is genuinely still building.
type blockingProvider struct {
	release chan struct{}
	inner   provider.Provider
}

func (p *blockingProvider) Name() string { return p.inner.Name() }

func (p *blockingProvider) Generate(ctx context.Context, prompt string, onStage func(string, string)) (provider.FileSet, error) {
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return p.inner.Generate(ctx, prompt, onStage)
}

func putFile(t *testing.T, base, id, rel, content string) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPut, base+"/api/sessions/"+id+"/file/"+rel, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put file: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func getFileBody(t *testing.T, base, id, rel string) string {
	t.Helper()
	resp, err := http.Get(base + "/api/sessions/" + id + "/file/" + rel)
	if err != nil {
		t.Fatalf("get file: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode file: %v", err)
	}
	return out.Content
}

// TestPutFileRebuildsAndRecordsTheEdit is the happy path: the write lands, a
// new build goes live, and the 事件流 carries the edit so the timeline shows it.
func TestPutFileRebuildsAndRecordsTheEdit(t *testing.T) {
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	const edited = "export default function App(){ return <main>手动改过</main> }"
	status, out := putFile(t, ts.URL, id, "src/App.tsx", edited)
	if status != http.StatusOK {
		t.Fatalf("put status = %d, want 200 (%v)", status, out)
	}
	if out["rebuilt"] != true {
		t.Fatalf("rebuilt = %v, want true (%v)", out["rebuilt"], out)
	}
	if _, ok := out["buildError"]; ok {
		t.Fatalf("unexpected buildError: %v", out)
	}
	if got := getFileBody(t, ts.URL, id, "src/App.tsx"); got != edited {
		t.Fatalf("file content = %q, want the edit", got)
	}

	// The edit is on the timeline, which is what tells the client to remount
	// the preview. Read the session rather than the SSE stream: the stream
	// helper stops at the first 完成 event, and this edit lands after it.
	events := sessionEvents(t, ts.URL, id)
	var found bool
	for _, ev := range events {
		if ev.Type == "file" && ev.Payload["edited"] == true && ev.Payload["path"] == "src/App.tsx" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no 编辑 file event on the stream: %+v", events)
	}

	// /app still serves a build (the rebuilt one).
	resp, err := http.Get(ts.URL + "/app/" + id + "/")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/app status = %d, want 200", resp.StatusCode)
	}
}

// TestPutFileRefusedWhileBuilding protects the 回合's own file writes: an edit
// landing mid-turn would be overwritten by the turn, or silently win against
// it — neither is a result the user could reason about.
func TestPutFileRefusedWhileBuilding(t *testing.T) {
	blocking := &blockingProvider{release: make(chan struct{}), inner: provider.NewTemplate()}
	ts := newTestServerWith(t, blocking)
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")

	status, _ := putFile(t, ts.URL, id, "src/App.tsx", "export default function App(){ return null }")
	if status != http.StatusConflict {
		t.Fatalf("put while building = %d, want 409", status)
	}

	close(blocking.release)
}

// TestPutFileRefusesPathsOutsideTheAppDir covers traversal and paths that do
// not exist: this is a source editor, not a file manager, and creating paths
// from here would let a member reach anything the process can write.
func TestPutFileRefusesPathsOutsideTheAppDir(t *testing.T) {
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	cases := []struct {
		name string
		path string
	}{
		// Fully escaped so the mux's own path cleaning cannot resolve it first:
		// this has to reach the handler as "../../outside.txt" and be refused
		// by the handler's own check.
		{"traversal", "%2e%2e%2f%2e%2e%2foutside.txt"},
		{"absolute", "%2fetc%2fpasswd"},
		{"missing file", "src/Nope.tsx"},
		{"a directory", "src"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, out := putFile(t, ts.URL, id, tc.path, "x")
			if status != http.StatusBadRequest && status != http.StatusNotFound {
				t.Fatalf("put %q = %d, want 400 or 404 (%v)", tc.path, status, out)
			}
		})
	}
}

// TestPutFileKeepsContentWhenTheRebuildFails is the failure the ticket calls
// out: the user's text must not be thrown away, the error must be visible, and
// the previous build must stay live so there is a way back.
func TestPutFileKeepsContentWhenTheRebuildFails(t *testing.T) {
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	const broken = "export default function App() { this is not valid tsx <<<"
	status, out := putFile(t, ts.URL, id, "src/App.tsx", broken)
	if status != http.StatusOK {
		t.Fatalf("put status = %d, want 200 — the write itself succeeded (%v)", status, out)
	}
	if out["rebuilt"] != false {
		t.Fatalf("rebuilt = %v, want false", out["rebuilt"])
	}
	if msg, _ := out["buildError"].(string); strings.TrimSpace(msg) == "" {
		t.Fatalf("buildError missing: %v", out)
	}

	// Nothing is lost: the text is on disk, so the user can fix it in place.
	if got := getFileBody(t, ts.URL, id, "src/App.tsx"); got != broken {
		t.Fatalf("content = %q, want the user's text preserved", got)
	}

	// The previous build is still what /app serves, so the app never goes dark
	// because an edit did not compile.
	resp, err := http.Get(ts.URL + "/app/" + id + "/")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/app status = %d after a failed rebuild, want 200", resp.StatusCode)
	}
}
