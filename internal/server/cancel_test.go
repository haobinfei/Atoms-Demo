package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

// newCancelTestServer exposes the Store so a case can arm the 取消回合 (Cancel
// Turn) flag the way a different replica's handler would — straight into the
// shared store, with no HTTP request on this server at all.
func newCancelTestServer(t *testing.T, prov provider.Provider) (*httptest.Server, session.Store) {
	t.Helper()
	dir := t.TempDir()
	store := session.NewStore(dir)
	srv := server.New(server.Config{WorkspaceDir: dir, DevAuth: true, Prov: prov, Store: store})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	return ts, store
}

func postChat(t *testing.T, tsURL, id, message string) int {
	t.Helper()
	body := strings.NewReader(fmt.Sprintf(`{"message":%q}`, message))
	resp, err := http.Post(tsURL+"/api/sessions/"+id+"/chat", "application/json", body)
	if err != nil {
		t.Fatalf("POST chat: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// sessionEvents reads the persisted 事件流 back off the session detail.
func sessionEvents(t *testing.T, tsURL, id string) []session.Event {
	t.Helper()
	resp, err := http.Get(tsURL + "/api/sessions/" + id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	defer resp.Body.Close()
	var sess struct {
		Events []session.Event `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return sess.Events
}

// previewStatus reports what /app/{id}/ answers, i.e. whether a version is
// still servable.
func previewStatus(t *testing.T, tsURL, id string) int {
	t.Helper()
	resp, err := http.Get(tsURL + "/app/" + id + "/")
	if err != nil {
		t.Fatalf("get preview: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestCancelStopsTheTurnAndKeepsThePreviousVersion covers the contract of a
// 取消回合 (Cancel Turn): the turn stops, the session goes back to 就绪 rather
// than 失败, a 取消 record is appended without removing what already happened,
// and the version built by the previous turn stays live (ADR-0013).
func TestCancelStopsTheTurnAndKeepsThePreviousVersion(t *testing.T) {
	ts := newTestServer(t)
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	first := readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)
	if got := sessionStatus(t, ts.URL, id); got != "ready" {
		t.Fatalf("status after the first 回合 = %q, want ready", got)
	}
	if code := previewStatus(t, ts.URL, id); code != http.StatusOK {
		t.Fatalf("preview before cancel = %d, want 200", code)
	}

	// Start a follow-up 回合 and cancel it immediately. It cannot finish
	// first: the three stages alone are gated by StepDelay, so the request is
	// always in flight while the turn is still running.
	if code := postChat(t, ts.URL, id, "把主题色换成绿色"); code != http.StatusOK {
		t.Fatalf("chat status = %d, want 200", code)
	}
	if code := post(t, ts.URL+"/api/sessions/"+id+"/cancel"); code != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200", code)
	}

	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", len(first), 40)

	var sawCancel, sawCanceledDone bool
	all := sessionEvents(t, ts.URL, id)
	if len(all) < len(first) {
		t.Fatal("cancel removed events that had already happened")
	}
	// Only the cancelled 回合's own slice: the first turn's file events are
	// expected and must be untouched.
	for _, ev := range all[len(first):] {
		switch {
		case ev.Type == session.CancelEventType:
			sawCancel = true
		case ev.Type == "done" && ev.Payload["canceled"] == true:
			sawCanceledDone = true
		case ev.Type == "file":
			t.Fatalf("a cancelled 回合 wrote files: %+v", ev)
		}
	}
	if !sawCancel {
		t.Fatal("no 取消 record on the 事件流")
	}
	if !sawCanceledDone {
		t.Fatal("no terminal 完成 event closing the cancelled 回合")
	}
	// A cancel is a user action, not a build failure.
	if got := sessionStatus(t, ts.URL, id); got != "ready" {
		t.Fatalf("status after cancel = %q, want ready", got)
	}
	// The abort happens before the file writes that commit a turn, so the
	// previous version is still exactly what /app serves.
	if code := previewStatus(t, ts.URL, id); code != http.StatusOK {
		t.Fatalf("preview after cancel = %d, want 200 (previous version must stay live)", code)
	}
}

// TestCancelIsRejectedOnAnIdleSession keeps cancel from becoming a no-op that
// still leaves a 取消 record behind: an unclosed record would arm the check
// and abort the *next* 回合.
func TestCancelIsRejectedOnAnIdleSession(t *testing.T) {
	ts := newTestServer(t)
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	if code := post(t, ts.URL+"/api/sessions/"+id+"/cancel"); code != http.StatusConflict {
		t.Fatalf("cancel on a ready session = %d, want 409", code)
	}
	if got := sessionStatus(t, ts.URL, id); got != "ready" {
		t.Fatalf("status after a rejected cancel = %q, want ready", got)
	}
	for _, ev := range sessionEvents(t, ts.URL, id) {
		if ev.Type == session.CancelEventType {
			t.Fatalf("a rejected cancel left a 取消 record behind: %+v", ev)
		}
	}
}

// TestCancelFlagIsReadFromTheStoreNotProcessMemory pins the ADR-0013 choice of
// a persisted flag over an in-process cancel: the request is armed by a
// *different* actor (here, straight into the shared store) and the replica
// executing the 回合 still honours it. That is what makes cancel survive
// several replicas and a restart.
func TestCancelFlagIsReadFromTheStoreNotProcessMemory(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	// Never strand the blocked Worker: Server.Close waits for its goroutines.
	t.Cleanup(release)

	ts, store := newCancelTestServer(t, gatedProvider{release: gate})
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	if got := sessionStatus(t, ts.URL, id); got != "building" {
		t.Fatalf("status = %q, want building (the 生成 is gated)", got)
	}

	if _, ok, err := store.RequestCancel("dev-user", id); err != nil || !ok {
		t.Fatalf("RequestCancel: ok=%v err=%v", ok, err)
	}

	release()
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	if got := sessionStatus(t, ts.URL, id); got != "ready" {
		t.Fatalf("status after cancel = %q, want ready", got)
	}
	var sawCanceledDone bool
	for _, ev := range sessionEvents(t, ts.URL, id) {
		if ev.Type == "file" {
			t.Fatalf("a cancelled 回合 wrote files: %+v", ev)
		}
		if ev.Type == "done" && ev.Payload["canceled"] == true {
			sawCanceledDone = true
		}
	}
	if !sawCanceledDone {
		t.Fatal("the 回合 was not aborted by the persisted flag")
	}
}

// TestCancelDoesNotLeakIntoTheNextTurn is the other half of the flag's
// semantics: the terminal 完成 event of a cancelled 回合 closes the window, so
// the next turn runs to completion instead of aborting on the old request.
func TestCancelDoesNotLeakIntoTheNextTurn(t *testing.T) {
	ts := newTestServer(t)
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	first := readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	if code := postChat(t, ts.URL, id, "把主题色换成绿色"); code != http.StatusOK {
		t.Fatalf("chat status = %d, want 200", code)
	}
	if code := post(t, ts.URL+"/api/sessions/"+id+"/cancel"); code != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200", code)
	}
	second := readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", len(first), 40)

	if code := postChat(t, ts.URL, id, "把标题改短"); code != http.StatusOK {
		t.Fatalf("second chat status = %d, want 200", code)
	}
	third := readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", len(first)+len(second), 40)

	var wroteFiles, canceled bool
	for _, ev := range third {
		switch {
		case ev.Type == "file":
			wroteFiles = true
		case ev.Type == "done" && ev.Payload["canceled"] == true:
			canceled = true
		}
	}
	if canceled {
		t.Fatal("the turn after a cancelled one was aborted by the stale request")
	}
	if !wroteFiles {
		t.Fatal("the turn after a cancelled one did not build")
	}
	if got := sessionStatus(t, ts.URL, id); got != "ready" {
		t.Fatalf("status = %q, want ready", got)
	}
}
