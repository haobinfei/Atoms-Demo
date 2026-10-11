package eventstream_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/haobinfei/Atoms-Demo/internal/eventstream"
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

func newMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return mr, client
}

func collect(t *testing.T, sub *eventstream.Subscription, n int) []session.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	out := make([]session.Event, 0, n)
	for len(out) < n {
		select {
		case ev := <-sub.Events():
			out = append(out, ev)
		case <-deadline:
			t.Fatalf("timed out waiting for %d events, got %d", n, len(out))
		}
	}
	return out
}

// TestRedisBusPublishesAndReplays covers the hot path: events land in the
// per-session Redis Stream, and a late subscriber replays only seq > after.
func TestRedisBusPublishesAndReplays(t *testing.T) {
	_, client := newMiniRedis(t)
	bus := eventstream.NewRedisBus(client)
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		ev := session.Event{Seq: i, Type: "agent", Agent: "mike"}
		if err := bus.Publish(ctx, "s1", ev); err != nil {
			t.Fatalf("publish seq %d: %v", i, err)
		}
	}
	if got := client.XLen(ctx, "atoms:events:s1").Val(); got != 5 {
		t.Fatalf("stream len = %d, want 5", got)
	}

	sub, err := bus.Subscribe(ctx, "s1", 2)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()
	got := collect(t, sub, 3)
	for i, ev := range got {
		if want := i + 3; ev.Seq != want {
			t.Fatalf("event %d seq = %d, want %d", i, ev.Seq, want)
		}
	}

	// Live tail: an event published after Subscribe still arrives.
	if err := bus.Publish(ctx, "s1", session.Event{Seq: 6, Type: "done"}); err != nil {
		t.Fatalf("publish seq 6: %v", err)
	}
	tail := collect(t, sub, 1)
	if tail[0].Seq != 6 {
		t.Fatalf("tail seq = %d, want 6", tail[0].Seq)
	}
}

// TestFanoutBusDegradesWhenRedisDown pins the ADR-0007 contract: Postgres is
// the source of truth, so a Redis outage must not fail publish and local
// subscribers must keep receiving events.
func TestFanoutBusDegradesWhenRedisDown(t *testing.T) {
	mr, client := newMiniRedis(t)
	bus := eventstream.NewFanoutBus(client)
	defer bus.Close()
	ctx := context.Background()

	sub, err := bus.Subscribe(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()

	mr.Close() // simulate a Redis outage after the stream was established
	if err := bus.Publish(ctx, "s1", session.Event{Seq: 1, Type: "plan"}); err != nil {
		t.Fatalf("publish must not fail when Redis is down: %v", err)
	}
	got := collect(t, sub, 1)
	if got[0].Seq != 1 {
		t.Fatalf("local fan-out lost the event: %+v", got[0])
	}
}

// TestMemoryBusSlowSubscriberAndClose ensures a stuck consumer cannot block
// the pipeline and that closing a subscription twice is safe.
func TestMemoryBusSlowSubscriberAndClose(t *testing.T) {
	bus := eventstream.NewMemoryBus()
	ctx := context.Background()

	stuck, err := bus.Subscribe(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer stuck.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; i <= 200; i++ { // far beyond the subscriber buffer
			if err := bus.Publish(ctx, "s1", session.Event{Seq: i}); err != nil {
				t.Errorf("publish %d: %v", i, err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publish blocked on a slow subscriber")
	}

	closed, err := bus.Subscribe(ctx, "s2", 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	closed.Close()
	closed.Close() // idempotent
	if err := bus.Publish(ctx, "s2", session.Event{Seq: 1}); err != nil {
		t.Fatalf("publish after close: %v", err)
	}
}
