package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
)

type testPublish struct {
	Version int    `json:"version"`
	URL     string `json:"url"`
}

// TestPublishVersionsAndHistory drives two publish rounds across a chat
// turn and asserts the full publish contract: public URL without
// credentials, monotonically increasing versions, complete history.
func TestPublishVersionsAndHistory(t *testing.T) {
	ts := newTestServerWith(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	first := readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	// Publish while a turn is running must be rejected.
	id2 := createSession(t, ts.URL, "一个数据看板")
	conflict, err := http.Post(ts.URL+"/api/sessions/"+id2+"/publish", "application/json", nil)
	if err != nil {
		t.Fatalf("publish while building: %v", err)
	}
	conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("publish while building status = %d, want 409", conflict.StatusCode)
	}

	// First publish: version 1, public URL serving turn-1 content.
	pub1 := publish(t, ts.URL, id)
	if pub1.Version != 1 {
		t.Fatalf("first publish version = %d, want 1", pub1.Version)
	}
	if pub1.URL != "/app/"+id+"/" {
		t.Fatalf("publish url = %q, want /app/%s/", pub1.URL, id)
	}
	resp, err := http.Get(ts.URL + pub1.URL)
	if err != nil {
		t.Fatalf("open published app: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("published app status = %d, want 200", resp.StatusCode)
	}
	assertServedCSS(t, ts.URL, id, "#f97316")

	// A chat turn changes the app; publishing again must version upward
	// and the public URL must actually serve the new turn's build.
	chatBody, _ := json.Marshal(map[string]string{"message": "把主题改成蓝色"})
	chatResp, err := http.Post(ts.URL+"/api/sessions/"+id+"/chat", "application/json", strings.NewReader(string(chatBody)))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	chatResp.Body.Close()
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", len(first), 60)
	publish(t, ts.URL, id)
	assertServedCSS(t, ts.URL, id, "#3b82f6")

	history := publishHistory(t, ts.URL, id)
	if len(history) != 2 {
		t.Fatalf("publish history len = %d, want 2", len(history))
	}
	if history[0].Version != 1 || history[1].Version != 2 {
		t.Fatalf("versions = %d,%d, want 1,2", history[0].Version, history[1].Version)
	}

	// A failed session has no app to publish: 400, and the failed status
	// must survive the rejected publish.
	failTS := newTestServerWith(t, erroringProvider{})
	badID := createSession(t, failTS.URL, "一个数据看板")
	readSSEUntilDone(t, failTS.URL+"/api/sessions/"+badID+"/events", 0, 40)
	noApp, err := http.Post(failTS.URL+"/api/sessions/"+badID+"/publish", "application/json", nil)
	if err != nil {
		t.Fatalf("publish failed session: %v", err)
	}
	noApp.Body.Close()
	if noApp.StatusCode != http.StatusBadRequest {
		t.Fatalf("publish failed session status = %d, want 400", noApp.StatusCode)
	}
	statusResp, err := http.Get(failTS.URL + "/api/sessions/" + badID)
	if err != nil {
		t.Fatalf("get failed session: %v", err)
	}
	var failedSess struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(statusResp.Body).Decode(&failedSess); err != nil {
		t.Fatalf("decode failed session: %v", err)
	}
	statusResp.Body.Close()
	if failedSess.Status != "failed" {
		t.Fatalf("failed session status = %q, want failed", failedSess.Status)
	}

	// Unknown session: 404 on both endpoints.
	missing, err := http.Post(ts.URL+"/api/sessions/nope/publish", "application/json", nil)
	if err != nil {
		t.Fatalf("publish missing: %v", err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("publish missing status = %d, want 404", missing.StatusCode)
	}
	getMissing, err := http.Get(ts.URL + "/api/sessions/nope/publish")
	if err != nil {
		t.Fatalf("get publish missing: %v", err)
	}
	getMissing.Body.Close()
	if getMissing.StatusCode != http.StatusNotFound {
		t.Fatalf("get publish missing status = %d, want 404", getMissing.StatusCode)
	}
}

// publish triggers a 发布 (Publish) and waits for the Worker to record the new
// version. The HTTP call is accepted immediately (202); the rebuild runs
// asynchronously, so we poll the publish history for the new entry.
func publish(t *testing.T, tsURL, id string) testPublish {
	t.Helper()
	before := len(publishHistory(t, tsURL, id))
	resp, err := http.Post(tsURL+"/api/sessions/"+id+"/publish", "application/json", nil)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("publish status = %d, want 202", resp.StatusCode)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		history := publishHistory(t, tsURL, id)
		if len(history) > before {
			return history[len(history)-1]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("publish did not record a new version within the deadline")
	return testPublish{}
}

func publishHistory(t *testing.T, tsURL, id string) []testPublish {
	t.Helper()
	resp, err := http.Get(tsURL + "/api/sessions/" + id + "/publish")
	if err != nil {
		t.Fatalf("publish history: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("publish history status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Publishes []testPublish `json:"publishes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	return out.Publishes
}

// TestPublishFailedRebuildKeepsServingDist breaks the app sources after a
// successful publish; the next publish must fail without taking down the
// version /app is serving, and the session must return to "ready".
func TestPublishFailedRebuildKeepsServingDist(t *testing.T) {
	ts, dir := newTestServerDir(t, provider.NewTemplate())
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)
	publish(t, ts.URL, id)

	// Corrupt the entry file in the import graph: esbuild cannot resolve
	// the import, so the rebuild fails.
	mainTSX := filepath.Join(dir, "dev-user", id, "app", "src", "main.tsx")
	if err := os.WriteFile(mainTSX, []byte(`import "./nope"`), 0o644); err != nil {
		t.Fatalf("corrupt sources: %v", err)
	}

	resp, err := http.Post(ts.URL+"/api/sessions/"+id+"/publish", "application/json", nil)
	if err != nil {
		t.Fatalf("publish after corruption: %v", err)
	}
	resp.Body.Close()
	// Publish is asynchronous now: the request is accepted, and the failure
	// surfaces as an error event with the session restored to "ready".
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("publish after corruption status = %d, want 202", resp.StatusCode)
	}
	waitForPublishFailure(t, ts.URL, id)

	// The previously published version is still served untouched.
	assertServedCSS(t, ts.URL, id, "#f97316")
}

// waitForPublishFailure polls the session until the async publish Worker has
// restored it to "ready" and appended an error event, or the deadline passes.
func waitForPublishFailure(t *testing.T, tsURL, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(tsURL + "/api/sessions/" + id)
		if err != nil {
			t.Fatalf("get session: %v", err)
		}
		var sess struct {
			Status string `json:"status"`
			Events []struct {
				Type    string         `json:"type"`
				Payload map[string]any `json:"payload"`
			} `json:"events"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
			resp.Body.Close()
			t.Fatalf("decode session: %v", err)
		}
		resp.Body.Close()
		if sess.Status != "ready" {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		for _, ev := range sess.Events {
			if ev.Type == "error" {
				if msg, _ := ev.Payload["message"].(string); strings.Contains(msg, "发布失败") {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("publish failure was not reported on the timeline within the deadline")
}

// assertServedCSS fetches the built stylesheet through the public /app URL
// and asserts it contains the given accent color.
func assertServedCSS(t *testing.T, tsURL, id, color string) {
	t.Helper()
	resp, err := http.Get(tsURL + "/app/" + id + "/main.css")
	if err != nil {
		t.Fatalf("get served css: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("served css status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read served css: %v", err)
	}
	if !strings.Contains(string(body), color) {
		t.Fatalf("served css missing %s", color)
	}
}

// erroringProvider fails every generation, driving the session into the
// "failed" state with no app directory on disk.
type erroringProvider struct{}

func (erroringProvider) Name() string { return "erroring" }

func (erroringProvider) Generate(_ context.Context, _ string, _ func(stage, detail string)) (provider.FileSet, error) {
	return nil, errors.New("generation unavailable")
}
