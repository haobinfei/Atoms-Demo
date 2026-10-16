package observability

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

// scopeName 是自建指标的 instrumentation scope。
const scopeName = "github.com/haobinfei/Atoms-Demo/internal/observability"

// 标签键。指标标签的取值集合都必须是有界枚举——任何租户、会话、账号标识都
// 不进标签（ADR-0018）。
var (
	AttrKind     = attribute.Key("kind")
	AttrReason   = attribute.Key("reason")
	AttrOutcome  = attribute.Key("outcome")
	AttrStage    = attribute.Key("stage")
	AttrStatus   = attribute.Key("status")
	AttrState    = attribute.Key("state")
	AttrScope    = attribute.Key("scope")
	AttrQueue    = attribute.Key("queue")
	AttrPool     = attribute.Key("pool")
	AttrOp       = attribute.Key("op")
	AttrProvider = attribute.Key("provider")
)

// MeasurementOption 是 metric.MeasurementOption 的别名：埋点调用方只需要依赖
// 本包，不必再 import otel/metric（包名与 sdk/metric 容易混）。
type MeasurementOption = metric.MeasurementOption

// WithString 是 metric.WithAttributes(attribute.String(...)) 的简写，让埋点
// 保持一行。
func WithString(k attribute.Key, v string) MeasurementOption {
	return metric.WithAttributes(attribute.String(string(k), v))
}

// Attrs 是多标签埋点的简写。
func Attrs(kvs ...attribute.KeyValue) MeasurementOption {
	return metric.WithAttributes(kvs...)
}

// 任务（Job）的种类与跳过原因等枚举值，集中在这里以免埋点各处写错字符串。
const (
	KindTurn    = "turn"
	KindContest = "contest"
	KindPublish = "publish"

	OutcomeOK          = "ok"
	OutcomeFailed      = "failed"
	OutcomeSuccess     = "success"
	OutcomeError       = "error"
	OutcomeFull        = "full"
	OutcomeClosed      = "closed"
	OutcomeInvalid     = "invalid"
	OutcomeUnavailable = "unavailable"
	OutcomeExceeded    = "exceeded"
	OutcomeCanceled    = "canceled"
	OutcomeReady       = "ready"

	ReasonSessionGone  = "session_missing"
	ReasonNotBuilding  = "not_building"
	ReasonKindMismatch = "kind_mismatch"
	ReasonDuplicate    = "duplicate"
	ReasonLeaseLost    = "lease_lost"
	ReasonHandlerError = "handler_error"
	ReasonMalformed    = "malformed"
)

// Instruments 是本服务自有的指标句柄集合。所有方法都应在 Setup 之后经 Obs()
// 取得——在 Setup 之前创建的句柄会永久绑定到 noop。
type Instruments struct {
	meter metric.Meter

	// 队列（Queue）与任务（Job）
	JobEnqueued    metric.Int64Counter
	JobEnqueueFail metric.Int64Counter
	JobDelivered   metric.Int64Counter
	JobAcknowledged metric.Int64Counter
	JobRedelivered metric.Int64Counter
	JobDuration    metric.Float64Histogram
	JobSkipped     metric.Int64Counter
	JobFailed      metric.Int64Counter
	JobRecovered   metric.Int64Counter

	// 回合（Turn）
	TurnFinished    metric.Int64Counter
	TurnStageFailed metric.Int64Counter

	// 生成引擎（Provider）
	ProviderAttempts metric.Int64Counter
	ProviderDuration metric.Float64Histogram
	ProviderFallback metric.Int64Counter

	// 构建（Build）
	BuildRuns             metric.Int64Counter
	BuildDuration         metric.Float64Histogram
	BuildCSSStubs         metric.Int64Counter
	BuildSelfHealExhausted metric.Int64Counter

	// 事件流（Event Stream）与 SSE
	SSEClients       metric.Int64UpDownCounter
	SSESubscribeFail metric.Int64Counter
	SSEReplays       metric.Int64Counter
	EventPublishFail metric.Int64Counter

	// 崩溃恢复与构建回收
	RecoverySweeps   metric.Int64Counter
	RecoveryRequeued metric.Int64Counter
	BuildGCRuns      metric.Int64Counter
	BuildGCRemoved   metric.Int64Counter

	// 业务计数
	SessionsCreated metric.Int64Counter
	Publishes       metric.Int64Counter
	PayOrders       metric.Int64Counter
	AuthRateLimited metric.Int64Counter

	// 产物存储（Artifact Store）
	ArtifactOps metric.Int64Counter
}

var (
	instOnce sync.Mutex
	inst     atomic.Pointer[Instruments]
)

// bind 在真实 MeterProvider 就位之后创建指标句柄。
func bind(mp metric.MeterProvider) *Instruments {
	instruments := newInstruments(mp.Meter(scopeName))
	inst.Store(instruments)
	return instruments
}

// Obs 返回指标句柄。稳态下是无锁读取；只有在 Setup 未被调用时（单元测试）才
// 走一次带锁的惰性创建，那时全局 provider 是 noop，句柄也就不会产生任何输出。
func Obs() *Instruments {
	if i := inst.Load(); i != nil {
		return i
	}
	instOnce.Lock()
	defer instOnce.Unlock()
	if i := inst.Load(); i != nil {
		return i
	}
	// Setup 可能尚未调用：绑定在当前全局 provider 上。若 Setup 随后运行，
	// 它会覆盖这里创建的结果。
	instruments := newInstruments(otel.Meter(scopeName))
	inst.Store(instruments)
	return instruments
}

func newInstruments(meter metric.Meter) *Instruments {
	i := &Instruments{meter: meter}

	i.JobEnqueued = int64Counter(meter, "atoms.job.enqueued", "进入队列的任务数", "{job}")
	i.JobEnqueueFail = int64Counter(meter, "atoms.job.enqueue.failed", "入队失败的任务数（队列满或 Redis 不可用）", "{job}")
	i.JobDelivered = int64Counter(meter, "atoms.job.delivered", "投递给 Worker 的任务数", "{job}")
	i.JobAcknowledged = int64Counter(meter, "atoms.job.acknowledged", "执行成功并已确认的任务数", "{job}")
	i.JobRedelivered = int64Counter(meter, "atoms.job.redelivered", "未被确认、等待重投的任务数（at-least-once）", "{job}")
	i.JobDuration = float64Histogram(meter, "atoms.job.duration", "单次任务从认领到结束的耗时", "s")
	i.JobSkipped = int64Counter(meter, "atoms.job.skipped", "认领后被跳过的任务数（重复投递或状态已变）", "{job}")
	i.JobFailed = int64Counter(meter, "atoms.job.failed", "执行出错、未确认的任务数", "{job}")
	i.JobRecovered = int64Counter(meter, "atoms.job.recovered", "崩溃恢复重跑的任务数", "{job}")

	i.TurnFinished = int64Counter(meter, "atoms.turn.finished", "回合结束数（按最终状态）", "{turn}")
	i.TurnStageFailed = int64Counter(meter, "atoms.turn.stage.failed", "回合在某个阶段失败", "{turn}")

	i.ProviderAttempts = int64Counter(meter, "atoms.provider.attempts", "生成引擎的调用次数", "{request}")
	i.ProviderDuration = float64Histogram(meter, "atoms.provider.duration", "单次生成引擎调用耗时", "s")
	i.ProviderFallback = int64Counter(meter, "atoms.provider.fallback", "模型链降级次数（上一环失败转到下一环）", "{request}")

	i.BuildRuns = int64Counter(meter, "atoms.build.runs", "构建次数（按结果）", "{build}")
	i.BuildDuration = float64Histogram(meter, "atoms.build.duration", "一次构建的耗时", "s")
	i.BuildCSSStubs = int64Counter(meter, "atoms.build.css.stubs", "构建自愈补出的空 CSS 桩数", "{file}")
	i.BuildSelfHealExhausted = int64Counter(meter, "atoms.build.selfheal.exhausted", "自愈轮次用尽仍未构建成功的次数", "{build}")

	i.SSEClients = int64UpDownCounter(meter, "atoms.sse.clients.active", "当前活跃的 SSE 连接数", "{client}")
	i.SSESubscribeFail = int64Counter(meter, "atoms.sse.subscribe.failed", "SSE 订阅失败次数", "{request}")
	i.SSEReplays = int64Counter(meter, "atoms.sse.replays", "从 Postgres 回放事件的次数（Redis 热路径降级信号）", "{replay}")
	i.EventPublishFail = int64Counter(meter, "atoms.event.publish.failed", "事件扇出到 Redis 失败次数", "{event}")

	i.RecoverySweeps = int64Counter(meter, "atoms.recovery.sweeps", "崩溃恢复扫描次数", "{sweep}")
	i.RecoveryRequeued = int64Counter(meter, "atoms.recovery.requeued", "崩溃恢复重新入队的任务数", "{job}")
	i.BuildGCRuns = int64Counter(meter, "atoms.buildgc.runs", "构建回收运行次数", "{run}")
	i.BuildGCRemoved = int64Counter(meter, "atoms.buildgc.removed", "构建回收删除的构建数", "{build}")

	i.SessionsCreated = int64Counter(meter, "atoms.sessions.created", "创建的会话数", "{session}")
	i.Publishes = int64Counter(meter, "atoms.publishes", "发布次数（按结果）", "{publish}")
	i.PayOrders = int64Counter(meter, "atoms.pay.orders", "模拟收银台订单数（按结果）", "{order}")
	i.AuthRateLimited = int64Counter(meter, "atoms.auth.rate.limited", "认证端点被限流拒绝的次数", "{request}")

	i.ArtifactOps = int64Counter(meter, "atoms.artifact.operations", "产物存储操作次数（按操作与结果）", "{operation}")

	return i
}

// Meter 暴露底层 meter，供同包的 observable 注册使用。
func (i *Instruments) Meter() metric.Meter {
	if i == nil || i.meter == nil {
		return metricnoop.NewMeterProvider().Meter(scopeName)
	}
	return i.meter
}

func int64Counter(meter metric.Meter, name, desc, unit string) metric.Int64Counter {
	c, err := meter.Int64Counter(name, metric.WithDescription(desc), metric.WithUnit(unit))
	if err != nil {
		slog.Error("create counter", "metric", name, "err", err)
		return metricnoop.Int64Counter{}
	}
	return c
}

func int64UpDownCounter(meter metric.Meter, name, desc, unit string) metric.Int64UpDownCounter {
	c, err := meter.Int64UpDownCounter(name, metric.WithDescription(desc), metric.WithUnit(unit))
	if err != nil {
		slog.Error("create updowncounter", "metric", name, "err", err)
		return metricnoop.Int64UpDownCounter{}
	}
	return c
}

func float64Histogram(meter metric.Meter, name, desc, unit string) metric.Float64Histogram {
	h, err := meter.Float64Histogram(name, metric.WithDescription(desc), metric.WithUnit(unit))
	if err != nil {
		slog.Error("create histogram", "metric", name, "err", err)
		return metricnoop.Float64Histogram{}
	}
	return h
}

func int64ObservableGauge(meter metric.Meter, name, desc, unit string) metric.Int64ObservableGauge {
	g, err := meter.Int64ObservableGauge(name, metric.WithDescription(desc), metric.WithUnit(unit))
	if err != nil {
		slog.Error("create observable gauge", "metric", name, "err", err)
		return metricnoop.Int64ObservableGauge{}
	}
	return g
}

func int64ObservableCounter(meter metric.Meter, name, desc, unit string) metric.Int64ObservableCounter {
	c, err := meter.Int64ObservableCounter(name, metric.WithDescription(desc), metric.WithUnit(unit))
	if err != nil {
		slog.Error("create observable counter", "metric", name, "err", err)
		return metricnoop.Int64ObservableCounter{}
	}
	return c
}
