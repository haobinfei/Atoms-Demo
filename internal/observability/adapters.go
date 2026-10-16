package observability

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// 这一层把外部依赖（Postgres 连接池、Redis 连接池、自建队列）的运行时状态接成
// OTel 的可观测（observable）回调。回调只在被采集时触发，所以不需要任何后台
// 轮询 goroutine —— 抓取间隔就是采样间隔。

// QueueDepth 是队列在抓取时刻的积压快照。
type QueueDepth struct {
	// Lag 是尚未投递给消费者组的条目数：真正的「排队等活」。
	Lag int64
	// Pending 是已投递但未确认的条目数：Worker 手上的活（含崩溃后等重投的）。
	Pending int64
	// Depth 是队列中的条目总数，Capacity 是容量上限。
	Depth    int64
	Capacity int64
}

// RegisterQueueDepth 注册队列积压指标。name 是队列名，作为 queue 标签。
//
// 回调返回错误时这一轮不上报：Redis 短暂不可用时把 0 当成「没有积压」比缺数据
// 更危险，会让告警静默。
func RegisterQueueDepth(name string, fn func(context.Context) (QueueDepth, error)) error {
	meter := Obs().Meter()
	lag := int64ObservableGauge(meter, "atoms.job.queue.lag", "排队中、尚未投递给 Worker 的任务数", "{job}")
	pending := int64ObservableGauge(meter, "atoms.job.queue.pending", "已投递但未确认的任务数（含等待重投）", "{job}")
	depth := int64ObservableGauge(meter, "atoms.job.queue.depth", "队列中的任务总数", "{job}")
	capacity := int64ObservableGauge(meter, "atoms.job.queue.capacity", "队列容量上限", "{job}")

	attrs := metric.WithAttributes(attribute.String(string(AttrQueue), name))
	_, err := meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		stats, err := fn(ctx)
		if err != nil {
			return nil
		}
		o.ObserveInt64(lag, stats.Lag, attrs)
		o.ObserveInt64(pending, stats.Pending, attrs)
		o.ObserveInt64(depth, stats.Depth, attrs)
		o.ObserveInt64(capacity, stats.Capacity, attrs)
		return nil
	}, lag, pending, depth, capacity)
	return err
}

// RegisterDBPool 注册一个 pgx 连接池的状态。stats 每次调用取一份快照。
func RegisterDBPool(name string, stats func() *pgxpool.Stat) error {
	meter := Obs().Meter()
	conns := int64ObservableGauge(meter, "atoms.db.pool.connections", "连接池中的连接数（按状态）", "{connection}")
	maxConns := int64ObservableGauge(meter, "atoms.db.pool.max.connections", "连接池上限", "{connection}")
	emptyAcquires := int64ObservableCounter(meter, "atoms.db.pool.empty.acquires", "因池中无空闲连接而等待的累计次数", "{acquire}")
	canceledAcquires := int64ObservableCounter(meter, "atoms.db.pool.canceled.acquires", "取连接时因取消而放弃的累计次数", "{acquire}")
	newConns := int64ObservableCounter(meter, "atoms.db.pool.new.connections", "累计新建的连接数", "{connection}")
	destroyed := int64ObservableCounter(meter, "atoms.db.pool.destroyed.connections", "累计销毁的连接数（按原因）", "{connection}")

	pool := attribute.String(string(AttrPool), name)
	_, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := stats()
		if s == nil {
			return nil
		}
		idle := metric.WithAttributes(pool, attribute.String(string(AttrState), "idle"))
		used := metric.WithAttributes(pool, attribute.String(string(AttrState), "used"))
		total := metric.WithAttributes(pool, attribute.String(string(AttrState), "total"))
		o.ObserveInt64(conns, int64(s.IdleConns()), idle)
		o.ObserveInt64(conns, int64(s.AcquiredConns()), used)
		o.ObserveInt64(conns, int64(s.TotalConns()), total)
		o.ObserveInt64(maxConns, int64(s.MaxConns()), metric.WithAttributes(pool))
		o.ObserveInt64(emptyAcquires, s.EmptyAcquireCount(), metric.WithAttributes(pool))
		o.ObserveInt64(canceledAcquires, s.CanceledAcquireCount(), metric.WithAttributes(pool))
		o.ObserveInt64(newConns, s.NewConnsCount(), metric.WithAttributes(pool))
		o.ObserveInt64(destroyed, s.MaxLifetimeDestroyCount(),
			metric.WithAttributes(pool, attribute.String(string(AttrReason), "max_lifetime")))
		o.ObserveInt64(destroyed, s.MaxIdleDestroyCount(),
			metric.WithAttributes(pool, attribute.String(string(AttrReason), "max_idle")))
		return nil
	}, conns, maxConns, emptyAcquires, canceledAcquires, newConns, destroyed)
	return err
}

// poolStatser 只匹配真正实现了连接池统计的客户端（*redis.Client /
// *redis.ClusterClient）；redis.UniversalClient 接口本身没有这个方法。
type poolStatser interface {
	PoolStats() *redis.PoolStats
}

// RegisterRedisPool 注册一个 go-redis 客户端的连接池状态。客户端不支持连接池
// 统计时返回 false，让调用方知道这个依赖没有指标（而不是静默什么都没有）。
func RegisterRedisPool(name string, client redis.UniversalClient) (bool, error) {
	statser, ok := client.(poolStatser)
	if !ok {
		return false, nil
	}
	meter := Obs().Meter()
	conns := int64ObservableGauge(meter, "atoms.redis.pool.connections", "Redis 连接池的连接数（按状态）", "{connection}")
	hits := int64ObservableCounter(meter, "atoms.redis.pool.hits", "复用空闲连接的累计次数", "{acquire}")
	misses := int64ObservableCounter(meter, "atoms.redis.pool.misses", "需要新建连接的累计次数", "{acquire}")
	timeouts := int64ObservableCounter(meter, "atoms.redis.pool.timeouts", "等待连接超时的累计次数", "{acquire}")
	waitCount := int64ObservableCounter(meter, "atoms.redis.pool.waits", "等待可用连接的累计次数", "{acquire}")

	pool := attribute.String(string(AttrPool), name)
	_, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := statser.PoolStats()
		if s == nil {
			return nil
		}
		idle := metric.WithAttributes(pool, attribute.String(string(AttrState), "idle"))
		used := metric.WithAttributes(pool, attribute.String(string(AttrState), "used"))
		stale := metric.WithAttributes(pool, attribute.String(string(AttrState), "stale"))
		pending := metric.WithAttributes(pool, attribute.String(string(AttrState), "pending"))
		o.ObserveInt64(conns, int64(s.IdleConns), idle)
		o.ObserveInt64(conns, int64(s.TotalConns)-int64(s.IdleConns), used)
		o.ObserveInt64(conns, int64(s.StaleConns), stale)
		o.ObserveInt64(conns, int64(s.PendingRequests), pending)
		o.ObserveInt64(hits, int64(s.Hits), metric.WithAttributes(pool))
		o.ObserveInt64(misses, int64(s.Misses), metric.WithAttributes(pool))
		o.ObserveInt64(timeouts, int64(s.Timeouts), metric.WithAttributes(pool))
		o.ObserveInt64(waitCount, int64(s.WaitCount), metric.WithAttributes(pool))
		return nil
	}, conns, hits, misses, timeouts, waitCount)
	return true, err
}
