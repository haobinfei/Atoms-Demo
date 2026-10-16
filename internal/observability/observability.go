// Package observability 装配 OpenTelemetry（ADR-0018）：metrics 恒开，以
// Prometheus 文本格式暴露在 /metrics 走 pull 模式；traces 由
// OTEL_EXPORTER_OTLP_ENDPOINT 开关，未配置时为 noop（零网络开销）。
//
// 配置全部走标准 OTEL_* 环境变量：
//
//	OTEL_SERVICE_NAME            服务名，默认 atoms-demo-server
//	OTEL_EXPORTER_OTLP_ENDPOINT  traces 上报地址（OTLP gRPC），留空 = 追踪 noop
//	OTEL_TRACES_SAMPLER_ARG      头部采样率 0~1，默认 0.1
//
// 指标命名沿用 OTel 语义约定（semconv）：通用指标用 semconv 名（如
// http.server.request.duration），本服务自有的指标用 atoms. 前缀。HTTP 指标的
// 路由标签取 net/http 的路由模板（r.Pattern），因此基数天然有界——不带也是
// 有意的：任何租户/会话标识都不进标签（ADR-0018）。
package observability

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/contrib/propagators/b3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/exemplar"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// MetricsPath 是 Prometheus 抓取端点。它挂在应用自己的端口上（不另开管理端口，
// ADR-0018）：web 侧的 nginx 只代理 ^/(api|app|pay)/，所以该路径不经公网暴露。
const MetricsPath = "/metrics"

// defaultServiceName 是 /metrics 与 span 上的资源属性 service.name，
// 可被 OTEL_SERVICE_NAME 覆盖。
const defaultServiceName = "atoms-demo-server"

// otlpEndpointEnv 是标准的 traces 上报地址变量；为空表示追踪关闭。
const otlpEndpointEnv = "OTEL_EXPORTER_OTLP_ENDPOINT"

// defaultSampleRatio 未配置 OTEL_TRACES_SAMPLER_ARG 时的头部采样率。
const defaultSampleRatio = 0.1

var (
	handlerMu      sync.Mutex
	metricsHandler http.Handler
	// metricsRegistry 由测试与调试使用；生产路径只经 Handler 暴露。
	metricsRegistry *prometheus.Registry
)

// Setup 初始化 MeterProvider（恒开）与 TracerProvider（按环境变量），返回
// 进程退出时冲刷缓冲的 shutdown 函数。
func Setup(ctx context.Context) (func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(defaultServiceName)),
		resource.WithFromEnv(), // OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES
		resource.WithTelemetrySDK(),
		resource.WithProcess(),
		resource.WithOS(),
	)
	if err != nil {
		return nil, fmt.Errorf("build otel resource: %w", err)
	}

	// 专用 registry 而不是默认 registry：默认 registry 自带 client_golang 的
	// Go/Process collector，再叠加 OTel runtime instrumentation 会得到两套语义
	// 重叠的运行时指标（AuthSphere 的 /metrics 上就是两套并存）。这里显式只留
	// 一套——用社区看板与告警都在用的经典 go_*/process_* 名。
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	exp, err := otelprom.New(
		otelprom.WithRegisterer(reg),
		// 丢掉 otel_scope_* 与 target_info：它们是常量，只是把每条序列撑肥。
		otelprom.WithoutScopeInfo(),
		otelprom.WithoutTargetInfo(),
	)
	if err != nil {
		return nil, fmt.Errorf("init prometheus exporter: %w", err)
	}

	mpOpts := []sdkmetric.Option{
		sdkmetric.WithReader(exp),
		sdkmetric.WithResource(res),
		sdkmetric.WithView(views()...),
	}
	if os.Getenv(otlpEndpointEnv) == "" {
		// traces 关闭时 exemplar 没有可关联的 trace id，留着它只有代价：
		//
		//  1. view 的 AttributeFilter 对 exemplar 不生效——OTel 明确规定 exemplar
		//     仍携带被 view 过滤掉的属性，于是标签集又变长；
		//  2. Prometheus 文本格式限制标签集 128 runes，超限就丢弃 exemplar 并打
		//     一行警告。实测线上因此把日志刷成了 100% 噪音（每条直方图序列每次
		//     采集都一行），真实错误完全看不见。
		//
		// 等 OTEL_EXPORTER_OTLP_ENDPOINT 打开、真正有 trace 可关联时再启用。
		mpOpts = append(mpOpts, sdkmetric.WithExemplarFilter(exemplar.AlwaysOffFilter))
	}
	mp := sdkmetric.NewMeterProvider(mpOpts...)
	otel.SetMeterProvider(mp)

	// 注入侧发多头格式的 b3，sidecar 也能提取；同时保留 W3C 与 Baggage，供
	// 非网格调用方使用。不修这条，应用 span 与网格 span 会断成两条 trace。
	otel.SetTextMapPropagator(newPropagator())

	handlerMu.Lock()
	metricsRegistry = reg
	metricsHandler = promhttp.InstrumentMetricHandler(reg, promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	handlerMu.Unlock()

	// 指标句柄必须绑定在真实 provider 之后创建，否则会永远绑到 noop。
	bind(mp)

	if os.Getenv(otlpEndpointEnv) == "" {
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
		return mp.Shutdown, nil
	}

	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("init OTLP trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRatio()))),
	)
	otel.SetTracerProvider(tp)

	return func(ctx context.Context) error {
		_ = tp.Shutdown(ctx)
		return mp.Shutdown(ctx)
	}, nil
}

// newPropagator 是 b3 + W3C TraceContext + Baggage 的三合一传播器。
func newPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		b3.New(), propagation.TraceContext{}, propagation.Baggage{},
	)
}

// sampleRatio 解析 OTEL_TRACES_SAMPLER_ARG（默认 0.1），非法值回退默认值。
func sampleRatio() float64 {
	raw := os.Getenv("OTEL_TRACES_SAMPLER_ARG")
	if raw == "" {
		return defaultSampleRatio
	}
	ratio, err := strconv.ParseFloat(raw, 64)
	if err != nil || ratio < 0 || ratio > 1 {
		return defaultSampleRatio
	}
	return ratio
}

// Handler 返回 /metrics 的处理器。Setup 之前调用会得到 503，让「指标没装配」
// 这件事在抓取侧可见，而不是返回一个空 body 骗过 Prometheus。
func Handler() http.Handler {
	handlerMu.Lock()
	defer handlerMu.Unlock()
	if metricsHandler == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "metrics not initialized", http.StatusServiceUnavailable)
		})
	}
	return metricsHandler
}

// Registry 返回指标注册表（测试与本地调试用）。
func Registry() prometheus.Gatherer {
	handlerMu.Lock()
	defer handlerMu.Unlock()
	if metricsRegistry == nil {
		return prometheus.NewRegistry()
	}
	return metricsRegistry
}

// Middleware 包住应用的 mux：一次装配同时给出 HTTP 服务端指标与 span。
//
// /metrics 自身被排除在埋点之外——抓取不该出现在它自己提供的指标里。事件流
// (SSE) 路由也一并排除：那条连接只结束于客户端断开，耗时是「用户看了多久」而不是
// 「服务用了多久」，放进请求耗时直方图会污染分位数；它有专门的
// atoms.sse.clients.active 与 atoms.sse.replays 表达。
//
// 入站请求的 http.route 取 net/http 的路由模板（r.Pattern）：ServeMux 会在
// handler 返回前把匹配到的模式写回 request，otelhttp 也在这之后才记录指标，
// 所以拿到的是 /api/sessions/{id} 而不是 /api/sessions/abc123。
func Middleware(next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, "",
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != MetricsPath && !IsStreamingPath(r.URL.Path)
		}),
		otelhttp.WithMetricAttributesFn(routeAttributes),
	)
}

// ssePathSuffix 是事件流路由的结尾。本服务只有 /api/sessions/{id}/events 一条
// 长连接路由，前缀匹配足够且不必把路由表搬进来。
const ssePathSuffix = "/events"

// IsStreamingPath 判断请求是否为长连接流式端点（不计入 HTTP 耗时指标）。
func IsStreamingPath(path string) bool {
	return strings.HasSuffix(path, ssePathSuffix)
}

// routeAttributes 把路由模板作为 http.route 标签；未匹配到任何路由的请求
// （404）归入 unmatched，避免把原始路径写进标签造成高基数。
func routeAttributes(r *http.Request) []attribute.KeyValue {
	route := r.Pattern
	if route == "" {
		route = "unmatched"
	}
	return []attribute.KeyValue{attrHTTPRoute.String(route)}
}

// views 配置两件事：压掉 HTTP 指标上的常量标签，以及为耗时直方图换上合适的
// 分桶。
//
// 分桶必须显式给：SDK 的默认边界是 [0,5,10,...,10000]，那是毫秒尺度的数字，
// 用在以秒为单位的直方图上会让所有亚秒延迟挤进 le=0、而尾部到 10000 秒——等于
// 分位数不可用（AuthSphere 的 /metrics 上就是这个形状）。
func views() []sdkmetric.View {
	return []sdkmetric.View{
		sdkmetric.NewView(
			sdkmetric.Instrument{Name: "http.server.*"},
			sdkmetric.Stream{
				AttributeFilter: attribute.NewAllowKeysFilter(
					attribute.Key("http.request.method"),
					attribute.Key("http.response.status_code"),
					attrHTTPRoute,
					attribute.Key("error.type"),
				),
				Aggregation: sdkmetric.AggregationExplicitBucketHistogram{Boundaries: httpBuckets},
			},
		),
		sdkmetric.NewView(
			sdkmetric.Instrument{Name: "atoms.job.duration"},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{Boundaries: generationBuckets}},
		),
		sdkmetric.NewView(
			sdkmetric.Instrument{Name: "atoms.turn.duration"},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{Boundaries: generationBuckets}},
		),
		sdkmetric.NewView(
			sdkmetric.Instrument{Name: "atoms.provider.duration"},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{Boundaries: generationBuckets}},
		),
		sdkmetric.NewView(
			sdkmetric.Instrument{Name: "atoms.build.duration"},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{Boundaries: buildBuckets}},
		),
	}
}

// httpBuckets 是 HTTP 请求耗时的秒级边界（semconv 推荐值）。
var httpBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}

// generationBuckets 覆盖一次生成（LLM 调用 30–300s 是常态）的量级。
var generationBuckets = []float64{0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600, 1800}

// buildBuckets 覆盖 esbuild 的量级（毫秒到十几秒）。
var buildBuckets = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30}

// attrHTTPRoute 是 semconv 的路由标签键。
const attrHTTPRoute = attribute.Key("http.route")
