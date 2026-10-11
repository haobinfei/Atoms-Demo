package jobqueue_test

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/haobinfei/Atoms-Demo/internal/jobqueue"
)

func newMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return mr, client
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestMemoryQueueOrderAndBound covers the in-process 队列: it delivers Jobs
// in FIFO order and refuses to grow past its capacity instead of blocking.
func TestMemoryQueueOrderAndBound(t *testing.T) {
	q := jobqueue.NewMemoryQueue(2)
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		if err := q.Enqueue(ctx, jobqueue.Job{Owner: "u", SessionID: id, Kind: jobqueue.KindTurn}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	if err := q.Enqueue(ctx, jobqueue.Job{SessionID: "c"}); !errors.Is(err, jobqueue.ErrQueueFull) {
		t.Fatalf("third enqueue err = %v, want ErrQueueFull", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	got := make(chan jobqueue.Job, 2)
	go func() {
		_ = q.Run(runCtx, "w1", func(_ context.Context, job jobqueue.Job) error {
			got <- job
			return nil
		})
	}()
	for _, want := range []string{"a", "b"} {
		select {
		case job := <-got:
			if job.SessionID != want {
				t.Fatalf("job order = %s, want %s", job.SessionID, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for job")
		}
	}
}

// TestMemoryQueueCloseIdempotent pins the shutdown contract: a closed queue
// rejects new work with ErrQueueClosed and tolerates a second Close.
func TestMemoryQueueCloseIdempotent(t *testing.T) {
	q := jobqueue.NewMemoryQueue(1)
	if err := q.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := q.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := q.Enqueue(context.Background(), jobqueue.Job{SessionID: "x"}); !errors.Is(err, jobqueue.ErrQueueClosed) {
		t.Fatalf("enqueue after close = %v, want ErrQueueClosed", err)
	}
}

// TestRedisQueueDeliversAndAcks covers the happy path: a Job is delivered
// exactly once and, once handled, leaves both the stream and the PEL.
func TestRedisQueueDeliversAndAcks(t *testing.T) {
	_, client := newMiniRedis(t)
	q := jobqueue.NewRedisQueue(client, jobqueue.WithCapacity(8), jobqueue.WithClaimIdle(0))
	ctx := context.Background()
	job := jobqueue.Job{Owner: "u", SessionID: "s1", Kind: jobqueue.KindTurn}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	got := make(chan jobqueue.Job, 1)
	go func() {
		_ = q.Run(runCtx, "w1", func(_ context.Context, j jobqueue.Job) error {
			got <- j
			return nil
		})
	}()

	select {
	case j := <-got:
		if j != job {
			t.Fatalf("job = %+v, want %+v", j, job)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for job delivery")
	}

	waitFor(t, "stream drained", func() bool {
		return client.XLen(ctx, jobqueue.DefaultStream).Val() == 0
	})
	waitFor(t, "pending drained", func() bool {
		pending := client.XPending(ctx, jobqueue.DefaultStream, jobqueue.DefaultGroup).Val()
		return pending == nil || pending.Count == 0
	})
}

// TestRedisQueueFull checks the atomic depth guard: once XLEN hits capacity,
// Enqueue returns ErrQueueFull instead of silently growing the stream.
func TestRedisQueueFull(t *testing.T) {
	_, client := newMiniRedis(t)
	q := jobqueue.NewRedisQueue(client, jobqueue.WithCapacity(1))
	ctx := context.Background()
	if err := q.Enqueue(ctx, jobqueue.Job{SessionID: "a", Kind: jobqueue.KindTurn}); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	if err := q.Enqueue(ctx, jobqueue.Job{SessionID: "b", Kind: jobqueue.KindTurn}); !errors.Is(err, jobqueue.ErrQueueFull) {
		t.Fatalf("second enqueue = %v, want ErrQueueFull", err)
	}
}

// TestRedisQueueAtLeastOnce pins the delivery guarantee: a handler that fails
// leaves the entry pending, and it is redelivered until it succeeds.
func TestRedisQueueAtLeastOnce(t *testing.T) {
	_, client := newMiniRedis(t)
	q := jobqueue.NewRedisQueue(client, jobqueue.WithCapacity(8), jobqueue.WithClaimIdle(0))
	ctx := context.Background()
	if err := q.Enqueue(ctx, jobqueue.Job{SessionID: "s1", Kind: jobqueue.KindTurn}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var calls int32
	handled := make(chan struct{})
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_ = q.Run(runCtx, "w1", func(_ context.Context, _ jobqueue.Job) error {
			if atomic.AddInt32(&calls, 1) == 1 {
				return errors.New("transient failure")
			}
			close(handled)
			return nil
		})
	}()

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for redelivery")
	}
	if got := atomic.LoadInt32(&calls); got < 2 {
		t.Fatalf("handler calls = %d, want >= 2 (redelivered)", got)
	}
}

// TestRedisQueueRunSurvivesOutage proves the consumer does not give up when
// Redis is unreachable: Run keeps retrying instead of exiting, and once Redis
// is back it delivers normally. Jobs live in the Redis Stream/PEL, not in a
// process-local channel, so an outage never drops them.
func TestRedisQueueRunSurvivesOutage(t *testing.T) {
	// Reserve an address, then release it so Redis is initially unreachable.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve addr: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	q := jobqueue.NewRedisQueue(client, jobqueue.WithCapacity(8), jobqueue.WithClaimIdle(0))

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan jobqueue.Job, 1)
	go func() {
		_ = q.Run(runCtx, "w1", func(_ context.Context, j jobqueue.Job) error {
			got <- j
			return nil
		})
	}()

	// Let the Worker hit the outage and start its retry loop.
	time.Sleep(300 * time.Millisecond)

	// Redis comes up on the same address.
	mr := miniredis.NewMiniRedis()
	if err := mr.StartAddr(addr); err != nil {
		t.Fatalf("start miniredis on %s: %v", addr, err)
	}
	t.Cleanup(mr.Close)

	if err := q.Enqueue(context.Background(), jobqueue.Job{SessionID: "s1", Kind: jobqueue.KindTurn}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	select {
	case job := <-got:
		if job.SessionID != "s1" {
			t.Fatalf("recovered job = %+v, want session s1", job)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for job after Redis came back")
	}
}
