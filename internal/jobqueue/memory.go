package jobqueue

import (
	"context"
	"sync"
)

// MemoryQueue is the bounded in-process 队列 for tests and Redis-less local
// runs. Capacity is fixed at construction; the server sizes it WorkerCount×4.
// Enqueue is non-blocking: at capacity it returns ErrQueueFull so the HTTP
// layer can answer 503 instead of blocking a request or dropping the Job.
type MemoryQueue struct {
	ch chan Job

	mu     sync.RWMutex
	closed bool
}

// NewMemoryQueue returns a MemoryQueue holding at most capacity Jobs.
func NewMemoryQueue(capacity int) *MemoryQueue {
	if capacity < 1 {
		capacity = 1
	}
	return &MemoryQueue{ch: make(chan Job, capacity)}
}

func (q *MemoryQueue) Enqueue(_ context.Context, job Job) error {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.closed {
		return ErrQueueClosed
	}
	select {
	case q.ch <- job:
		return nil
	default:
		return ErrQueueFull
	}
}

// Run drains the shared channel. Multiple Run goroutines form the Worker pool:
// each Job is delivered to exactly one of them.
func (q *MemoryQueue) Run(ctx context.Context, _ string, handle Handler) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case job, ok := <-q.ch:
			if !ok {
				return nil
			}
			_ = handle(ctx, job)
		}
	}
}

// Close is idempotent. Enqueue after Close returns ErrQueueClosed; Run drains
// whatever is buffered, then returns.
func (q *MemoryQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil
	}
	q.closed = true
	close(q.ch)
	return nil
}
