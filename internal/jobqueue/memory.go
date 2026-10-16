package jobqueue

import (
	"context"
	"sync"

	"github.com/haobinfei/Atoms-Demo/internal/observability"
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

func (q *MemoryQueue) Enqueue(ctx context.Context, job Job) error {
	obs := observability.Obs()
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.closed {
		obs.JobEnqueueFail.Add(ctx, 1, failAttrs(job.Kind, observability.OutcomeClosed))
		return ErrQueueClosed
	}
	select {
	case q.ch <- job:
		obs.JobEnqueued.Add(ctx, 1, kindAttr(job.Kind))
		return nil
	default:
		obs.JobEnqueueFail.Add(ctx, 1, failAttrs(job.Kind, observability.OutcomeFull))
		return ErrQueueFull
	}
}

// Run drains the shared channel. Multiple Run goroutines form the Worker pool:
// each Job is delivered to exactly one of them.
func (q *MemoryQueue) Run(ctx context.Context, _ string, handle Handler) error {
	obs := observability.Obs()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case job, ok := <-q.ch:
			if !ok {
				return nil
			}
			obs.JobDelivered.Add(ctx, 1, kindAttr(job.Kind))
			if err := handle(ctx, job); err != nil {
				obs.JobRedelivered.Add(ctx, 1, failAttrs(job.Kind, observability.ReasonHandlerError))
				continue
			}
			obs.JobAcknowledged.Add(ctx, 1, kindAttr(job.Kind))
		}
	}
}

// Stats reports the buffered backlog. There is no PEL in-process, so nothing is
// ever "delivered but unacknowledged": Lag and Depth are the same number.
func (q *MemoryQueue) Stats(context.Context) (Stats, error) {
	return Stats{Lag: len(q.ch), Depth: len(q.ch), Capacity: cap(q.ch)}, nil
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
