package server_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/artifact"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

type failingRecordStore struct {
	session.Store
	fail atomic.Bool
}

func (s *failingRecordStore) RecordPublish(owner, id, url string) (session.Publish, error) {
	if s.fail.Load() {
		return session.Publish{}, errors.New("injected publish record failure")
	}
	return s.Store.RecordPublish(owner, id, url)
}

func newSharedArtifactServers(t *testing.T, cfg server.Config) (*httptest.Server, *httptest.Server) {
	t.Helper()
	a := server.New(cfg)
	b := server.New(cfg)
	tsA := httptest.NewServer(a.Handler())
	tsB := httptest.NewServer(b.Handler())
	t.Cleanup(func() {
		tsA.Close()
		tsB.Close()
		_ = a.Close()
		_ = b.Close()
	})
	return tsA, tsB
}

// TestServerReplicasSharePublishedArtifacts proves that a preview uploaded by
// one replica and a publish activated by that replica are immediately visible
// through another replica's /app handler.
func TestServerReplicasSharePublishedArtifacts(t *testing.T) {
	dir := t.TempDir()
	store := session.NewStore(dir)
	artifacts := artifact.New(artifact.NewMemoryBackend())
	tsA, tsB := newSharedArtifactServers(t, server.Config{
		WorkspaceDir: dir,
		Store:        store,
		Artifacts:    artifacts,
		DevAuth:      true,
		Prov:         provider.NewTemplate(),
	})

	id := createSession(t, tsA.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, tsA.URL+"/api/sessions/"+id+"/events", 0, 40)

	// The first turn's preview is uploaded by replica A and served by B.
	resp, err := http.Get(tsB.URL + "/app/" + id + "/")
	if err != nil {
		t.Fatalf("replica B preview: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read replica B preview: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `id="root"`) {
		t.Fatalf("replica B preview status=%d body=%q", resp.StatusCode, string(body))
	}
	if strings.Contains(string(body), "/src/main.tsx") || !strings.Contains(string(body), `src="./main.js"`) {
		t.Fatalf("replica B preview does not load the built bundle: %q", string(body))
	}

	// Change the source directly so only the publish path can make the new
	// version visible, then verify replica B serves the published build.
	cssPath := filepath.Join(dir, "dev-user", id, "app", "src", "styles.css")
	raw, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("read source css: %v", err)
	}
	raw = []byte(strings.ReplaceAll(string(raw), "#f97316", "#3b82f6"))
	if err := os.WriteFile(cssPath, raw, 0o644); err != nil {
		t.Fatalf("write source css: %v", err)
	}
	publish(t, tsA.URL, id)
	assertServedCSS(t, tsB.URL, id, "#3b82f6")
}

// TestPublishRecordFailureKeepsPreviousArtifact verifies the publish rollback
// contract when the immutable build was activated but the durable publish
// record fails: /app must remain on the previous build and its pointer.
func TestPublishRecordFailureKeepsPreviousArtifact(t *testing.T) {
	dir := t.TempDir()
	base := session.NewStore(dir)
	store := &failingRecordStore{Store: base}
	artifacts := artifact.New(artifact.NewMemoryBackend())
	srv := server.New(server.Config{
		WorkspaceDir: dir,
		Store:        store,
		Artifacts:    artifacts,
		DevAuth:      true,
		Prov:         provider.NewTemplate(),
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})

	id := createSession(t, ts.URL, "一家咖啡店的电商网站")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)
	publish(t, ts.URL, id)

	firstID, ok, err := artifacts.Current(context.Background(), artifact.SessionPrefix(id))
	if err != nil || !ok {
		t.Fatalf("current before failed publish: id=%q ok=%v err=%v", firstID, ok, err)
	}
	assertServedCSS(t, ts.URL, id, "#f97316")

	// Change source without running a turn, so the next publish stages a
	// different build while the current pointer still points at orange.
	cssPath := filepath.Join(dir, "dev-user", id, "app", "src", "styles.css")
	raw, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("read source css: %v", err)
	}
	raw = []byte(strings.ReplaceAll(string(raw), "#f97316", "#3b82f6"))
	if err := os.WriteFile(cssPath, raw, 0o644); err != nil {
		t.Fatalf("write source css: %v", err)
	}

	store.fail.Store(true)
	resp, err := http.Post(ts.URL+"/api/sessions/"+id+"/publish", "application/json", nil)
	if err != nil {
		t.Fatalf("publish with record failure: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("publish status = %d, want 202", resp.StatusCode)
	}
	waitForPublishFailure(t, ts.URL, id)

	current, ok, err := artifacts.Current(context.Background(), artifact.SessionPrefix(id))
	if err != nil || !ok {
		t.Fatalf("current after failed publish: id=%q ok=%v err=%v", current, ok, err)
	}
	if current != firstID {
		t.Fatalf("current after rollback = %q, want previous %q", current, firstID)
	}
	assertServedCSS(t, ts.URL, id, "#f97316")
}

// TestContestWinnerVisibleOnReplica proves contest archives and the winner
// copy use the same shared artifact store across replicas.
func TestContestWinnerVisibleOnReplica(t *testing.T) {
	dir := t.TempDir()
	store := session.NewStore(dir)
	artifacts := artifact.New(artifact.NewMemoryBackend())
	a := &contestStub{name: "contestant-a", marker: "VariantA"}
	b := &contestStub{name: "contestant-b", marker: "VariantB"}
	tsA, tsB := newSharedArtifactServers(t, server.Config{
		WorkspaceDir: dir,
		Store:        store,
		Artifacts:    artifacts,
		DevAuth:      true,
		Prov:         provider.FakeProvider{},
		Competitors:  []provider.Provider{a, b},
		ScoreFunc:    fixedScore,
		QueueWorkers: 1,
	})

	id := createContest(t, tsA.URL, "一个数据看板")
	readSSEUntilDone(t, tsA.URL+"/api/sessions/"+id+"/events", 0, 60)
	if status := pickWinner(t, tsA.URL, id, "contestant-b"); status != http.StatusOK {
		t.Fatalf("pick winner status = %d, want 200", status)
	}

	for _, path := range []string{"/app/" + id + "/", "/app/" + id + "/contest/contestant-a/"} {
		resp, err := http.Get(tsB.URL + path)
		if err != nil {
			t.Fatalf("replica B GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("replica B GET %s status = %d, want 200", path, resp.StatusCode)
		}
	}
	resp, err := http.Get(tsB.URL + "/app/" + id + "/main.js")
	if err != nil {
		t.Fatalf("replica B winner bundle: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read winner bundle: %v", err)
	}
	if !strings.Contains(string(body), "VariantB") {
		t.Fatalf("winner bundle on replica B missing VariantB: %q", string(body))
	}
}
