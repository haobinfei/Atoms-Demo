package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

// contestStub is a deterministic 参赛者: it sleeps so tests can prove the
// race is parallel, then returns a marked but valid React file set.
type contestStub struct {
	name   string
	marker string
	delay  time.Duration

	mu      sync.Mutex
	started time.Time
	ended   time.Time
}

func (c *contestStub) Name() string { return c.name }

func (c *contestStub) Generate(_ context.Context, _ string, _ func(string, string)) (provider.FileSet, error) {
	c.mu.Lock()
	c.started = time.Now()
	c.mu.Unlock()
	time.Sleep(c.delay)
	c.mu.Lock()
	c.ended = time.Now()
	c.mu.Unlock()
	return provider.FileSet{
		"src/App.tsx":  "export default function App() {\n  return <h1>" + c.marker + "</h1>\n}\n",
		"src/main.tsx": "import { createRoot } from 'react-dom/client'\nimport App from './App'\ncreateRoot(document.getElementById('root')!).render(<App />)\n",
		"index.html":   "<!doctype html>\n<html><body><div id=\"root\"></div></body></html>\n",
	}, nil
}

func (c *contestStub) window() (start, end time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started, c.ended
}

// newContestServer wires a server with explicit contestants and a stub
// Nina so scoring is deterministic.
func newContestServer(t *testing.T, competitors []provider.Provider, score server.ScoreFunc) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	srv := server.New(server.Config{
		WorkspaceDir: dir,
		DevAuth:      true,
		Prov:         provider.FakeProvider{},
		Competitors:  competitors,
		ScoreFunc:    score,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	return ts, dir
}

func createContest(t *testing.T, tsURL, prompt string) string {
	t.Helper()
	body := `{"prompt":"` + prompt + `","competition":true}`
	resp, err := http.Post(tsURL+"/api/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create contest: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create contest status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.SessionID
}

func contestState(t *testing.T, tsURL, id string) *session.Contest {
	t.Helper()
	resp, err := http.Get(tsURL + "/api/sessions/" + id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	defer resp.Body.Close()
	var sess struct {
		Contest *session.Contest `json:"contest"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return sess.Contest
}

func pickWinner(t *testing.T, tsURL, id, providerName string) int {
	t.Helper()
	resp, err := http.Post(tsURL+"/api/sessions/"+id+"/winner",
		"application/json", strings.NewReader(`{"provider":"`+providerName+`"}`))
	if err != nil {
		t.Fatalf("pick winner: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestContestParallelScoringAndWinner drives a full competition: parallel
// generation (proven by overlapping windows), Nina's score cards, loser
// archives and the winner pick that installs the official app.
func TestContestParallelScoringAndWinner(t *testing.T) {
	a := &contestStub{name: "contestant-a", marker: "VariantA", delay: 200 * time.Millisecond}
	b := &contestStub{name: "contestant-b", marker: "VariantB", delay: 200 * time.Millisecond}
	scoreCalls := 0
	ts, _ := newContestServer(t,
		[]provider.Provider{a, erroringProvider{}, b},
		func(_ context.Context, _ string, files provider.FileSet) (session.Scores, string, error) {
			scoreCalls++
			marker := "?"
			for _, content := range files {
				if strings.Contains(content, "VariantA") {
					marker = "A"
				}
				if strings.Contains(content, "VariantB") {
					marker = "B"
				}
			}
			return session.Scores{Design: 8, Functionality: 7, CodeQuality: 9},
				"点评 " + marker, nil
		})
	id := createContest(t, ts.URL, "一个数据看板")

	started := time.Now()
	events := readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 60)
	elapsed := time.Since(started)

	// Parallelism: the two healthy contestants overlap; three sequential
	// 200ms runs plus scoring could never finish this fast.
	aStart, aEnd := a.window()
	bStart, bEnd := b.window()
	if !aStart.Before(bEnd) || !bStart.Before(aEnd) {
		t.Fatalf("contestants did not overlap: a=%v..%v b=%v..%v", aStart, aEnd, bStart, bEnd)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("contest took %v; scoring never finished", elapsed)
	}

	// Nina scored exactly the two ready versions (the failed one skipped).
	if scoreCalls != 2 {
		t.Fatalf("score calls = %d, want 2", scoreCalls)
	}
	var sawScore bool
	for _, ev := range events {
		if ev.Type == "score" {
			sawScore = true
		}
	}
	if !sawScore {
		t.Fatal("no score events on the timeline")
	}

	// Contest state: graceful field reduction — one failed, two ready with
	// three-dimension scores and comments.
	contest := contestState(t, ts.URL, id)
	if contest == nil || !contest.Done {
		t.Fatalf("contest state = %+v", contest)
	}
	if len(contest.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(contest.Entries))
	}
	ready := map[string]session.ContestEntry{}
	for _, entry := range contest.Entries {
		switch entry.Provider {
		case "contestant-a", "contestant-b":
			if entry.Status != "ready" || entry.Scores == nil {
				t.Fatalf("entry %s = %+v", entry.Provider, entry)
			}
			ready[entry.Provider] = entry
		case "erroring":
			if entry.Status != "failed" {
				t.Fatalf("failed entry = %+v", entry)
			}
		}
	}
	if ready["contestant-a"].Scores.Design != 8 || ready["contestant-a"].Comment == "" {
		t.Fatalf("contestant-a scores = %+v", ready["contestant-a"].Scores)
	}

	// Loser versions are publicly reviewable at their own URLs.
	for _, name := range []string{"contestant-a", "contestant-b"} {
		resp, err := http.Get(ts.URL + "/app/" + id + "/contest/" + name + "/")
		if err != nil {
			t.Fatalf("open %s version: %v", name, err)
		}
		body := make([]byte, 4096)
		n, _ := resp.Body.Read(body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s version status = %d", name, resp.StatusCode)
		}
		_ = n
	}
	js, err := http.Get(ts.URL + "/app/" + id + "/contest/contestant-a/main.js")
	if err != nil {
		t.Fatalf("open contestant-a bundle: %v", err)
	}
	jsBody, err := io.ReadAll(js.Body)
	js.Body.Close()
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	if !strings.Contains(string(jsBody), "VariantA") {
		t.Fatal("contestant-a bundle missing its marker")
	}

	// Root URL serves nothing until a winner is picked.
	root, err := http.Get(ts.URL + "/app/" + id + "/")
	if err != nil {
		t.Fatalf("root before winner: %v", err)
	}
	root.Body.Close()
	if root.StatusCode != http.StatusNotFound {
		t.Fatalf("root before winner status = %d, want 404", root.StatusCode)
	}

	// Winner pick: rejects invalid and double picks, then installs.
	if code := pickWinner(t, ts.URL, id, "erroring"); code != http.StatusBadRequest {
		t.Fatalf("pick failed contestant status = %d, want 400", code)
	}
	if code := pickWinner(t, ts.URL, id, "contestant-b"); code != http.StatusOK {
		t.Fatalf("pick winner status = %d, want 200", code)
	}
	if code := pickWinner(t, ts.URL, id, "contestant-a"); code != http.StatusConflict {
		t.Fatalf("double pick status = %d, want 409", code)
	}
	contest = contestState(t, ts.URL, id)
	if contest.Winner != "contestant-b" {
		t.Fatalf("winner = %q", contest.Winner)
	}
	winnerApp, err := http.Get(ts.URL + "/app/" + id + "/main.js")
	if err != nil {
		t.Fatalf("winner app: %v", err)
	}
	wb, err := io.ReadAll(winnerApp.Body)
	winnerApp.Body.Close()
	if err != nil {
		t.Fatalf("read winner bundle: %v", err)
	}
	if !strings.Contains(string(wb), "VariantB") {
		t.Fatal("official app is not the winner's build")
	}

	// Iteration after the winner: chat turn runs against the installed app.
	chatBody := `{"message":"把主题改成蓝色"}`
	chatResp, err := http.Post(ts.URL+"/api/sessions/"+id+"/chat", "application/json", strings.NewReader(chatBody))
	if err != nil {
		t.Fatalf("chat after winner: %v", err)
	}
	chatResp.Body.Close()
	if chatResp.StatusCode != http.StatusOK {
		t.Fatalf("chat after winner status = %d", chatResp.StatusCode)
	}
}

// TestContestAllFailedStillFinishes: even with zero ready versions the
// contest completes, the session turns ready and picking is refused —
// nothing wedges.
func TestContestAllFailedStillFinishes(t *testing.T) {
	ts, _ := newContestServer(t,
		[]provider.Provider{erroringProvider{}, erroringProvider{}},
		func(context.Context, string, provider.FileSet) (session.Scores, string, error) {
			t.Fatal("scorer must not run with no ready versions")
			return session.Scores{}, "", nil
		})
	id := createContest(t, ts.URL, "一个数据看板")
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)

	contest := contestState(t, ts.URL, id)
	if contest == nil || !contest.Done || len(contest.Entries) != 2 {
		t.Fatalf("contest state = %+v", contest)
	}
	for _, entry := range contest.Entries {
		if entry.Status != "failed" {
			t.Fatalf("entry = %+v", entry)
		}
	}
	if code := pickWinner(t, ts.URL, id, "erroring"); code != http.StatusBadRequest {
		t.Fatalf("pick with no ready contestants status = %d, want 400", code)
	}
}

// TestContestWinnerRequiresFinishedContest: picking mid-race is a conflict.
func TestContestWinnerRequiresFinishedContest(t *testing.T) {
	slow := &contestStub{name: "slow", marker: "VariantS", delay: 300 * time.Millisecond}
	ts, _ := newContestServer(t, []provider.Provider{slow}, nil)
	id := createContest(t, ts.URL, "一个数据看板")
	if code := pickWinner(t, ts.URL, id, "slow"); code != http.StatusConflict {
		t.Fatalf("pick during race status = %d, want 409", code)
	}
	readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 40)
	if code := pickWinner(t, ts.URL, id, "slow"); code != http.StatusOK {
		t.Fatalf("pick after race status = %d, want 200", code)
	}
}
