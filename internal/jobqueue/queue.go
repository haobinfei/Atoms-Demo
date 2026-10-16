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
	// Recover marks a Job re-enqueued by the crash-recovery sweeper; the
	// Worker emits a 恢复事件 (recovered event) before re-running the work.
	Recover bool `json:"recover,omitempty"`
}

// Handler executes one Job. Returning an error leaves the Job
// unacknowledged (Redis redelivers it, at-least-once); returning nil
// acknowledges it.
type Handler func(ctx context.Context, job Job) error

// Stats 是队列在某一刻的积压快照，供 /metrics 抓取时读取（observable
// callback，不需要后台轮询 goroutine）。
type Stats struct {
	// Lag 是尚未投递给消费者组的条目数：真正「排队等活」的部分。
	Lag int
	// Pending 是已投递但未确认的条目数：Worker 手上的活，含崩溃后等待重投的。
	//
	// Lag 与 Pending 对应完全不同的故障：Lag 涨说明 Worker 不够或吞吐掉了；
	// Pending 涨说明执行慢或 Worker 在崩（at-least-once 的堆积）。
	Pending int
	// Depth 是队列中的条目总数，Capacity 是容量上限。
	Depth    int
	Capacity int
}

// Queue is the 队列 contract. Enqueue never blocks on a full queue: a full or
// closed queue is a hard error the caller surfaces as 503 rather than a
// silent drop. Run blocks draining until ctx is cancelled or the queue closes.
type Queue interface {
	Enqueue(ctx context.Context, job Job) error
	Run(ctx context.Context, workerID string, handle Handler) error
	// Stats reports the current backlog. It is read-only and may be called from
	// a metrics scrape; implementations must not block on it.
	Stats(ctx context.Context) (Stats, error)
	Close() error
}

var (
	// ErrQueueFull means the bounded queue is at capacity; callers answer 503.
	ErrQueueFull = errors.New("jobqueue: queue full")
	// ErrQueueClosed means the queue is shutting down; callers answer 503.
	ErrQueueClosed = errors.New("jobqueue: queue closed")
)
