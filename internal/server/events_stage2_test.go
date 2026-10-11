package server_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/eventstream"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

// sseStream is a background-parsed event stream so a test can wait for the
// next event with a timeout instead of blocking forever on a stuck handler.
type sseStream struct {
	events chan session.Event
}

func openSSE(t *testing.T, url string) *sseStream {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	s := &sseStream{events: make(chan session.Event, 64)}
	go func() {
		defer close(s.events)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var ev session.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				return
			}
			s.events <- ev
		}
	}()
	return s
}

func (s *sseStream) next(t *testing.T, timeout time.Duration) session.Event {
	t.Helper()
	select {
	case ev, ok := <-s.events:
		if !ok {
			t.Fatal("event stream closed unexpectedly")
		}
		return ev
	case <-time.After(timeout):
		t.Fatal("timed out waiting for SSE event")
	}
	return session.Event{}
}

func stage2Server(t *testing.T, poll time.Duration) (*httptest.Server, session.Store, eventstream.Bus) {
	t.Helper()
	dir := t.TempDir()
	st := session.NewStore(dir)
	bus := eventstream.NewMemoryBus()
	srv := server.New(server.Config{
		WorkspaceDir:      dir,
		Store:             st,
		Bus:               bus,
		DevAuth:           true,
		Prov:              provider.FakeProvider{},
		HeartbeatInterval: time.Hour, // keep comment lines out of the way
		PollInterval:      poll,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	return ts, st, bus
}

// TestEventsGapFillFromPostgres proves the source of truth tops up a hole on
// the live hot path: seq+1 is persisted but never published to the bus, then
// a live seq+2 exposes the gap and the handler backfills before writing it.
func TestEventsGapFillFromPostgres(t *testing.T) {
	ts, st, bus := stage2Server(t, time.Hour)
	id := createSession(t, ts.URL, "缺口回填")
	done := readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 20)
	maxSeq := done[len(done)-1].Seq

	stream := openSSE(t, ts.URL+"/api/sessions/"+id+"/events?after="+strconv.Itoa(maxSeq))

	dropped, err := st.AppendEvent("dev-user", id, session.Event{Type: "agent", Agent: "alex"})
	if err != nil {
		t.Fatalf("append dropped event: %v", err)
	}
	tail, err := st.AppendEvent("dev-user", id, session.Event{Type: "done", Agent: "sarah"})
	if err != nil {
		t.Fatalf("append tail event: %v", err)
	}
	// Publish only the tail: the bus hot path skips the dropped seq.
	if err := bus.Publish(t.Context(), id, tail); err != nil {
		t.Fatalf("publish tail: %v", err)
	}

	first := stream.next(t, 3*time.Second)
	if first.Seq != dropped.Seq || first.Type != "agent" {
		t.Fatalf("first live event = (%d,%s), want (%d,agent)", first.Seq, first.Type, dropped.Seq)
	}
	second := stream.next(t, 3*time.Second)
	if second.Seq != tail.Seq || second.Type != "done" {
		t.Fatalf("second live event = (%d,%s), want (%d,done)", second.Seq, second.Type, tail.Seq)
	}
}

// TestEventsPollingFallback covers Redis being down entirely: the client is
// caught up, no live event is published, and the Postgres poll still delivers
// newly persisted events.
func TestEventsPollingFallback(t *testing.T) {
	ts, st, _ := stage2Server(t, 30*time.Millisecond)
	id := createSession(t, ts.URL, "轮询兜底")
	done := readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 20)
	maxSeq := done[len(done)-1].Seq

	stream := openSSE(t, ts.URL+"/api/sessions/"+id+"/events?after="+strconv.Itoa(maxSeq))
	ev, err := st.AppendEvent("dev-user", id, session.Event{Type: "agent", Agent: "nina"})
	if err != nil {
		t.Fatalf("append event: %v", err)
	}
	got := stream.next(t, 3*time.Second)
	if got.Seq != ev.Seq || got.Type != "agent" {
		t.Fatalf("polled event = (%d,%s), want (%d,agent)", got.Seq, got.Type, ev.Seq)
	}
}

// TestEventsHeartbeatOnIdleStream pins the proxy-keepalive requirement: a
// caught-up connection with no events still receives SSE comment lines.
func TestEventsHeartbeatOnIdleStream(t *testing.T) {
	dir := t.TempDir()
	srv := server.New(server.Config{
		WorkspaceDir:      dir,
		Store:             session.NewStore(dir),
		Bus:               eventstream.NewMemoryBus(),
		DevAuth:           true,
		Prov:              provider.FakeProvider{},
		HeartbeatInterval: 40 * time.Millisecond,
		PollInterval:      time.Hour,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})

	id := createSession(t, ts.URL, "心跳保活")
	done := readSSEUntilDone(t, ts.URL+"/api/sessions/"+id+"/events", 0, 20)
	maxSeq := done[len(done)-1].Seq

	resp, err := http.Get(ts.URL + "/api/sessions/" + id + "/events?after=" + strconv.Itoa(maxSeq))
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	defer resp.Body.Close()

	lines := make(chan string, 8)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	select {
	case line, ok := <-lines:
		if !ok {
			t.Fatal("event stream closed before any heartbeat")
		}
		if !strings.HasPrefix(line, ": ping") {
			t.Fatalf("first line = %q, want SSE heartbeat comment", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SSE heartbeat")
	}
}
