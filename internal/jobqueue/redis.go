package jobqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/haobinfei/Atoms-Demo/internal/observability"
)

// Default stream/group names for the shared 队列.
const (
	DefaultStream = "atoms:jobs"
	DefaultGroup  = "atoms:workers"
)

// RedisQueue is the production 队列: one Redis Stream plus a consumer group.
// Each Worker is a consumer in the group. An entry stays pending until its
// handler succeeds, so a crash or a Redis blip redelivers instead of dropping
// (at-least-once). Postgres remains the source of truth for session state, so
// a duplicate delivery is harmless: the Worker-side status claim discards it.
type RedisQueue struct {
	client    redis.UniversalClient
	stream    string
	group     string
	capacity  int
	claimIdle time.Duration
	readCount int64
}

// RedisOption customizes a RedisQueue.
type RedisOption func(*RedisQueue)

// WithNames overrides the stream and consumer-group names.
func WithNames(stream, group string) RedisOption {
	return func(q *RedisQueue) {
		if stream != "" {
			q.stream = stream
		}
		if group != "" {
			q.group = group
		}
	}
}

// WithCapacity bounds the queue depth (XLEN). Enqueue past it returns
// ErrQueueFull. It must be > 0.
func WithCapacity(n int) RedisOption {
	return func(q *RedisQueue) {
		if n > 0 {
			q.capacity = n
		}
	}
}

// WithClaimIdle sets how long an entry may sit unacknowledged before another
// Worker may XAUTOCLAIM it (recovering a crashed consumer). Zero claims
// immediately.
func WithClaimIdle(d time.Duration) RedisOption {
	return func(q *RedisQueue) {
		if d < 0 {
			d = 0
		}
		q.claimIdle = d
	}
}

// NewRedisQueue builds a Redis Streams queue over client.
func NewRedisQueue(client redis.UniversalClient, opts ...RedisOption) *RedisQueue {
	q := &RedisQueue{
		client:    client,
		stream:    DefaultStream,
		group:     DefaultGroup,
		capacity:  64,
		claimIdle: 30 * time.Second,
		readCount: 16,
	}
	for _, opt := range opts {
		opt(q)
	}
	return q
}

// enqueueScript atomically checks the depth and appends, so two concurrent
// Enqueues can never both slip past a full queue. Returns the new entry ID, or
// the integer 0 when the queue is already at capacity.
var enqueueScript = redis.NewScript(`
if redis.call('XLEN', KEYS[1]) >= tonumber(ARGV[1]) then
  return 0
end
return redis.call('XADD', KEYS[1], '*', 'job', ARGV[2])
`)

// Enqueue appends one Job. A full queue (ErrQueueFull) or an unreachable Redis
// (wrapped connection error) is returned to the caller, which answers 503 —
// the queue never silently falls back to a lossy in-process channel.
func (q *RedisQueue) Enqueue(ctx context.Context, job Job) error {
	obs := observability.Obs()
	if q == nil || q.client == nil {
		obs.JobEnqueueFail.Add(ctx, 1, failAttrs(job.Kind, observability.OutcomeUnavailable))
		return errors.New("jobqueue: redis client is nil")
	}
	payload, err := json.Marshal(job)
	if err != nil {
		obs.JobEnqueueFail.Add(ctx, 1, failAttrs(job.Kind, observability.OutcomeInvalid))
		return err
	}
	res, err := enqueueScript.Run(ctx, q.client, []string{q.stream}, q.capacity, payload).Result()
	if err != nil {
		// Redis 不可用：这是 503 的来源，必须能看见。
		obs.JobEnqueueFail.Add(ctx, 1, failAttrs(job.Kind, observability.OutcomeUnavailable))
		return err
	}
	switch v := res.(type) {
	case int64:
		if v == 0 {
			obs.JobEnqueueFail.Add(ctx, 1, failAttrs(job.Kind, observability.OutcomeFull))
			return ErrQueueFull
		}
	case string:
		if v == "" {
			obs.JobEnqueueFail.Add(ctx, 1, failAttrs(job.Kind, observability.OutcomeFull))
			return ErrQueueFull
		}
	default:
		obs.JobEnqueueFail.Add(ctx, 1, failAttrs(job.Kind, observability.OutcomeInvalid))
		return fmt.Errorf("jobqueue: unexpected enqueue reply %T", res)
	}
	obs.JobEnqueued.Add(ctx, 1, kindAttr(job.Kind))
	return nil
}

// Stats reports the current backlog. Lag comes from the consumer group's
// undelivered count, Pending from the PEL: they answer different questions
// (Workers too few vs. execution too slow or crashing).
func (q *RedisQueue) Stats(ctx context.Context) (Stats, error) {
	if q == nil || q.client == nil {
		return Stats{}, errors.New("jobqueue: redis client is nil")
	}
	depth, err := q.client.XLen(ctx, q.stream).Result()
	if err != nil {
		return Stats{}, err
	}
	stats := Stats{Depth: int(depth), Capacity: q.capacity}
	if depth == 0 {
		// An empty stream has no lag and no pending entries; skip XINFO, which
		// errors on a stream that does not exist yet.
		return stats, nil
	}
	groups, err := q.client.XInfoGroups(ctx, q.stream).Result()
	if err != nil {
		return Stats{}, err
	}
	for _, group := range groups {
		if group.Name != q.group {
			continue
		}
		if group.Lag >= 0 {
			stats.Lag = int(group.Lag)
		}
		stats.Pending = int(group.Pending)
	}
	return stats, nil
}

// Run consumes Jobs as the named consumer until ctx is cancelled. A Redis that
// was down at startup (or that drops mid-run) is retried forever rather than
// exiting: pending entries survive in the PEL, so recovery is just reconnect.
func (q *RedisQueue) Run(ctx context.Context, workerID string, handle Handler) error {
	if q == nil || q.client == nil {
		return errors.New("jobqueue: redis client is nil")
	}
	if workerID == "" {
		workerID = "worker"
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := q.ensureGroup(ctx); err != nil {
			slog.Warn("jobqueue: create consumer group", "stream", q.stream, "err", err)
			if !sleep(ctx, time.Second) {
				return ctx.Err()
			}
			continue
		}
		// Recover abandoned entries first: an entry whose consumer crashed
		// (or whose XACK failed during an outage) is reclaimed once idle.
		claimed, _, err := q.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   q.stream,
			Group:    q.group,
			Consumer: workerID,
			MinIdle:  q.claimIdle,
			Start:    "0-0",
			Count:    q.readCount,
		}).Result()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Warn("jobqueue: autoclaim", "stream", q.stream, "err", err)
			if !sleep(ctx, time.Second) {
				return ctx.Err()
			}
			continue
		}
		if len(claimed) > 0 {
			q.process(ctx, claimed, handle)
			continue
		}
		streams, err := q.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    q.group,
			Consumer: workerID,
			Streams:  []string{q.stream, ">"},
			Count:    q.readCount,
			Block:    time.Second,
		}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Warn("jobqueue: read group", "stream", q.stream, "err", err)
			if !sleep(ctx, time.Second) {
				return ctx.Err()
			}
			continue
		}
		for _, stream := range streams {
			q.process(ctx, stream.Messages, handle)
		}
	}
}

// process runs each message's handler and acknowledges only on success. A
// failed handler leaves the entry pending for redelivery; a malformed entry is
// acked and dropped so it cannot poison the stream forever.
func (q *RedisQueue) process(ctx context.Context, msgs []redis.XMessage, handle Handler) {
	obs := observability.Obs()
	for _, msg := range msgs {
		if ctx.Err() != nil {
			return
		}
		job, ok := decodeJob(msg.Values["job"])
		if !ok {
			slog.Warn("jobqueue: dropping malformed job", "id", msg.ID)
			obs.JobRedelivered.Add(ctx, 1, observability.WithString(observability.AttrReason, observability.ReasonMalformed))
			q.ack(ctx, msg.ID)
			continue
		}
		obs.JobDelivered.Add(ctx, 1, kindAttr(job.Kind))
		if err := handle(ctx, job); err != nil {
			slog.Warn("jobqueue: handler failed; leaving pending", "id", msg.ID, "kind", job.Kind, "err", err)
			obs.JobRedelivered.Add(ctx, 1, failAttrs(job.Kind, observability.ReasonHandlerError))
			continue
		}
		obs.JobAcknowledged.Add(ctx, 1, kindAttr(job.Kind))
		q.ack(ctx, msg.ID)
	}
}

// kindAttr is the one-label option every 任务 (Job) metric carries.
func kindAttr(kind Kind) observability.MeasurementOption {
	return observability.WithString(observability.AttrKind, string(kind))
}

// failAttrs labels a failed 任务 (Job) with its kind and a bounded reason.
func failAttrs(kind Kind, reason string) observability.MeasurementOption {
	return observability.Attrs(
		observability.AttrKind.String(string(kind)),
		observability.AttrReason.String(reason),
	)
}

func (q *RedisQueue) ack(ctx context.Context, id string) {
	// ACK clears the PEL entry; XDEL frees the stream slot so XLEN (the
	// capacity gauge) drops back down.
	if err := q.client.XAck(ctx, q.stream, q.group, id).Err(); err != nil {
		slog.Warn("jobqueue: xack", "stream", q.stream, "id", id, "err", err)
		return
	}
	if err := q.client.XDel(ctx, q.stream, id).Err(); err != nil {
		slog.Warn("jobqueue: xdel", "stream", q.stream, "id", id, "err", err)
	}
}

func (q *RedisQueue) ensureGroup(ctx context.Context) error {
	// Start at 0 so Jobs enqueued before the group existed are still delivered.
	err := q.client.XGroupCreateMkStream(ctx, q.stream, q.group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

// Close is a no-op: the Redis client is owned by the caller (the server closes
// it) and Run exits on ctx cancellation. Keeping Close side-effect free lets
// the Queue and the event Bus share one client without a double close.
func (q *RedisQueue) Close() error { return nil }

func decodeJob(raw any) (Job, bool) {
	var data []byte
	switch v := raw.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	case json.RawMessage:
		data = v
	default:
		return Job{}, false
	}
	var job Job
	if err := json.Unmarshal(data, &job); err != nil || job.SessionID == "" {
		return Job{}, false
	}
	return job, true
}

// sleep waits d or until ctx is done, reporting whether the full wait elapsed.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
