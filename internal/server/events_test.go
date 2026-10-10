package server_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/session"
)

// readSSEEvents opens the event stream and collects n SSE data payloads,
// each parsed as a session.Event.
func readSSEEvents(t *testing.T, url string, n int, after int) []session.Event {
	t.Helper()
	if after > 0 {
		url = fmt.Sprintf("%s?after=%d", url, after)
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	var events []session.Event
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() && len(events) < n {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev session.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("decode event %q: %v", line, err)
		}
		events = append(events, ev)
	}
	if len(events) < n {
		t.Fatalf("got %d events, want %d", len(events), n)
	}
	return events
}

func createSession(t *testing.T, tsURL, prompt string) string {
	t.Helper()
	resp, err := http.Post(tsURL+"/api/sessions", "application/json",
		strings.NewReader(fmt.Sprintf(`{"prompt":%q}`, prompt)))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.SessionID
}

// readSSEUntilDone collects events until one has type "done" (or the cap
// is hit), so the pipeline goroutine has finished before the test asserts
// on files and before t.TempDir cleanup runs.
func readSSEUntilDone(t *testing.T, url string, cap int) []session.Event {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	defer resp.Body.Close()
	var events []session.Event
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() && len(events) < cap {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev session.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		events = append(events, ev)
		if ev.Type == "done" {
			return events
		}
	}
	t.Fatalf("no done event within %d events", cap)
	return nil
}

func TestEventStreamEmitsPipelineInOrder(t *testing.T) {
	ts := newTestServer(t)
	id := createSession(t, ts.URL, "一家咖啡店的电商网站")

	url := ts.URL + "/api/sessions/" + id + "/events"
	// Fake pipeline for this slice: plan → spec → architecture → 3 files → done.
	events := readSSEEvents(t, url, 7, 0)

	wantTypes := []string{"plan", "spec", "architecture", "file", "file", "file", "done"}
	wantAgents := []string{"mike", "emma", "bob", "alex", "alex", "alex", "sarah"}
	for i, ev := range events {
		if ev.Type != wantTypes[i] || ev.Agent != wantAgents[i] {
			t.Fatalf("event %d = (%s,%s), want (%s,%s)", i, ev.Type, ev.Agent, wantTypes[i], wantAgents[i])
		}
		if ev.Seq != i+1 {
			t.Fatalf("event %d seq = %d, want %d", i, ev.Seq, i+1)
		}
	}

	// Resume: ?after=2 must replay from seq 3 without duplicates.
	resumed := readSSEEvents(t, url, 2, 2)
	if resumed[0].Seq != 3 || resumed[1].Seq != 4 {
		t.Fatalf("resume starts at seq %d, want 3", resumed[0].Seq)
	}

	// After the pipeline finishes the session is ready.
	get, err := http.Get(ts.URL + "/api/sessions/" + id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	defer get.Body.Close()
	var sess session.Session
	if err := json.NewDecoder(get.Body).Decode(&sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if sess.Status != "ready" {
		t.Fatalf("status = %q, want ready", sess.Status)
	}
}
