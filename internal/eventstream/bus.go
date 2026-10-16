// Package eventstream fans session events out to SSE clients. Postgres
// remains the append-only source of truth; Redis Streams is the best-effort
// cross-replica hot path, and an in-process memory bus keeps single-replica
// behavior working when Redis is unavailable.
package eventstream

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/haobinfei/Atoms-Demo/internal/observability"
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

// Subscription is one live event consumer. Events are ordered on a
// best-effort basis; consumers must dedupe by Event.Seq and replay gaps
// from the session store.
type Subscription struct {
	events <-chan session.Event
	close  func()
}

func (s *Subscription) Events() <-chan session.Event { return s.events }

func (s *Subscription) Close() {
	if s != nil && s.close != nil {
		s.close()
	}
}

// Bus is the event fan-out seam used by the pipeline and SSE handler.
type Bus interface {
	Publish(ctx context.Context, sessionID string, ev session.Event) error
	Subscribe(ctx context.Context, sessionID string, after int) (*Subscription, error)
	Close() error
}

// MemoryBus is the single-process fallback. It never fails publish: slow
// subscribers are dropped and resume through the persisted event stream.
type MemoryBus struct {
	mu   sync.RWMutex
	subs map[string]map[chan session.Event]struct{}
}

func NewMemoryBus() *MemoryBus {
	return &MemoryBus{subs: map[string]map[chan session.Event]struct{}{}}
}

func (b *MemoryBus) Publish(_ context.Context, sessionID string, ev session.Event) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs[sessionID] {
		select {
		case ch <- ev:
		default: // slow client: resume from the persisted stream via ?after=
		}
	}
	return nil
}

func (b *MemoryBus) Subscribe(ctx context.Context, sessionID string, _ int) (*Subscription, error) {
	ch := make(chan session.Event, 32)
	b.mu.Lock()
	if b.subs[sessionID] == nil {
		b.subs[sessionID] = map[chan session.Event]struct{}{}
	}
	b.subs[sessionID][ch] = struct{}{}
	b.mu.Unlock()

	done := make(chan struct{})
	var once sync.Once
	closeFn := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs[sessionID], ch)
			if len(b.subs[sessionID]) == 0 {
				delete(b.subs, sessionID)
			}
			b.mu.Unlock()
			close(done)
		})
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		closeFn()
	}()
	return &Subscription{events: ch, close: closeFn}, nil
}

func (b *MemoryBus) Close() error { return nil }

// RedisBus writes events to a per-session Redis Stream and reads them back
// for cross-replica fan-out. Redis is deliberately not the source of truth.
type RedisBus struct {
	client redis.UniversalClient
	prefix string
}

func NewRedisBus(client redis.UniversalClient) *RedisBus {
	return &RedisBus{client: client, prefix: "atoms:events:"}
}

func (b *RedisBus) key(sessionID string) string { return b.prefix + sessionID }

func (b *RedisBus) Publish(ctx context.Context, sessionID string, ev session.Event) error {
	if b == nil || b.client == nil {
		return errors.New("redis client is nil")
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	// Cap the stream: Postgres is the source of truth, so a trimmed entry
	// only costs live latency — a subscriber's ?after= replay refills it.
	return b.client.XAdd(ctx, &redis.XAddArgs{
		Stream: b.key(sessionID),
		MaxLen: 1000,
		Approx: true,
		Values: map[string]any{"event": payload},
	}).Err()
}

func (b *RedisBus) Subscribe(ctx context.Context, sessionID string, after int) (*Subscription, error) {
	if b == nil || b.client == nil {
		return nil, errors.New("redis client is nil")
	}
	readCtx, cancel := context.WithCancel(ctx)
	ch := make(chan session.Event, 64)
	go b.read(readCtx, sessionID, after, ch)
	var once sync.Once
	return &Subscription{
		events: ch,
		close:  func() { once.Do(cancel) },
	}, nil
}

func (b *RedisBus) read(ctx context.Context, sessionID string, after int, out chan<- session.Event) {
	lastID := "0-0"
	for {
		if ctx.Err() != nil {
			return
		}
		streams, err := b.client.XRead(ctx, &redis.XReadArgs{
			Streams: []string{b.key(sessionID), lastID},
			Count:   100,
			Block:   time.Second,
		}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			slog.Warn("redis event stream read", "session", sessionID, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		for _, stream := range streams {
			for _, msg := range stream.Messages {
				lastID = msg.ID
				ev, ok := decodeEvent(msg.Values["event"])
				if !ok || ev.Seq <= after {
					continue
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

func (b *RedisBus) Close() error {
	if b == nil || b.client == nil {
		return nil
	}
	return b.client.Close()
}

func decodeEvent(raw any) (session.Event, bool) {
	var data []byte
	switch v := raw.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	case json.RawMessage:
		data = v
	default:
		return session.Event{}, false
	}
	var ev session.Event
	if err := json.Unmarshal(data, &ev); err != nil {
		return session.Event{}, false
	}
	return ev, true
}

// FanoutBus keeps local subscribers immediate while forwarding Redis Stream
// events into the same in-process path. Redis failures are logged and
// degrade to local fan-out plus the caller's Postgres replay/polling path.
type FanoutBus struct {
	memory *MemoryBus
	redis  *RedisBus
}

// NewFanoutBus returns the production composite bus: an in-process MemoryBus
// for same-replica immediacy plus Redis Streams for cross-replica fan-out.
// A nil client yields memory-only fan-out.
func NewFanoutBus(client redis.UniversalClient) *FanoutBus {
	bus := &FanoutBus{memory: NewMemoryBus()}
	if client != nil {
		bus.redis = NewRedisBus(client)
	}
	return bus
}

func (b *FanoutBus) Publish(ctx context.Context, sessionID string, ev session.Event) error {
	if b == nil || b.memory == nil {
		return errors.New("event bus is nil")
	}
	_ = b.memory.Publish(ctx, sessionID, ev)
	if b.redis != nil {
		if err := b.redis.Publish(ctx, sessionID, ev); err != nil {
			// Redis 热路径失败不改语义（事件已落 Postgres），但它是「实时性降级」
			// 的唯一信号：这个数一涨，SSE 就开始靠回放兜底了。
			observability.Obs().EventPublishFail.Add(ctx, 1)
			slog.Warn("redis event publish", "session", sessionID, "err", err)
		}
	}
	// Redis is a hot path, not the source of truth. The event is already
	// persisted by the caller; a Redis failure must not fail generation.
	return nil
}

func (b *FanoutBus) Subscribe(ctx context.Context, sessionID string, after int) (*Subscription, error) {
	if b == nil || b.memory == nil {
		return nil, errors.New("event bus is nil")
	}
	local, err := b.memory.Subscribe(ctx, sessionID, after)
	if err != nil {
		return nil, err
	}
	if b.redis == nil {
		return local, nil
	}
	remote, err := b.redis.Subscribe(ctx, sessionID, after)
	if err != nil {
		return local, nil
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-remote.Events():
				if !ok {
					return
				}
				_ = b.memory.Publish(ctx, sessionID, ev)
			}
		}
	}()
	var once sync.Once
	return &Subscription{
		events: local.Events(),
		close: func() {
			once.Do(func() {
				remote.Close()
				local.Close()
			})
		},
	}, nil
}

func (b *FanoutBus) Close() error {
	if b == nil {
		return nil
	}
	if b.redis != nil {
		return b.redis.Close()
	}
	return nil
}
