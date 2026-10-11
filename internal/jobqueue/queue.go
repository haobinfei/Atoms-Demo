// Package jobqueue is the 队列 (Queue) seam: turn/contest/publish 任务 (Job)
// enter a bounded buffer and a Worker pool drains it. MemoryQueue backs tests
// and Redis-less local runs; RedisQueue uses Redis Streams + a consumer group
// so Workers can span replicas (ADR-0007). Postgres stays the source of
// truth — a Job carries only a pointer to the session, never the payload.
package jobqueue

import (
	"context"
	"errors"
)

// Kind enumerates the three 任务 (Job) types. The value is the scheduling
// unit, not the domain process: a turn Job is one 回合.
type Kind string

const (
	KindTurn    Kind = "turn"
	KindContest Kind = "contest"
	KindPublish Kind = "publish"
)

// Job is the minimal pointer a Worker needs: who owns the session, which
// session, and which kind of work. Every other input is re-read from the
// store, so consuming the same pointer twice is naturally idempotent.
type Job struct {
	Owner     string `json:"owner"`
	SessionID string `json:"sessionId"`
	Kind      Kind   `json:"kind"`
}

// Handler executes one Job. Returning an error leaves the Job
// unacknowledged (Redis redelivers it, at-least-once); returning nil
// acknowledges it.
type Handler func(ctx context.Context, job Job) error

// Queue is the 队列 contract. Enqueue never blocks on a full queue: a full or
// closed queue is a hard error the caller surfaces as 503 rather than a
// silent drop. Run blocks draining until ctx is cancelled or the queue closes.
type Queue interface {
	Enqueue(ctx context.Context, job Job) error
	Run(ctx context.Context, workerID string, handle Handler) error
	Close() error
}

var (
	// ErrQueueFull means the bounded queue is at capacity; callers answer 503.
	ErrQueueFull = errors.New("jobqueue: queue full")
	// ErrQueueClosed means the queue is shutting down; callers answer 503.
	ErrQueueClosed = errors.New("jobqueue: queue closed")
)
