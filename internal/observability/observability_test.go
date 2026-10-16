package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
)

// 这组测试是 ADR-0018 的可执行版本：指标名、标签基数与分桶都是长期契约，
// 改动会让 Grafana 看板静默失效，所以要用测试钉住。

// newTestHandler 复刻生产的装配：应用 mux + /metrics + 最外层观测中间件。
func newTestHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		Handler().ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /api/sessions/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return Middleware(mux)
}

func setupForTest(t *testing.T) (http.Handler, func()) {
	t.Helper()
	shutdown, err := Setup(context.Background())
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	return newTestHandler(), func() { _ = shutdown(context.Background()) }
}

func do(t *testing.T, h http.Handler, path string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code >= 500 {
		t.Fatalf("GET %s: status %d", path, rec.Code)
	}
}

func gather(t *testing.T) map[string]*dto.MetricFamily {
	t.Helper()
	families, err := Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	byName := make(map[string]*dto.MetricFamily, len(families))
	for _, f := range families {
		byName[f.GetName()] = f
	}
	return byName
}

func labelValue(m *dto.Metric, name string) (string, bool) {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue(), true
		}
	}
	return "", false
}

// TestHTTPRouteLabelIsBounded 是本轮最重要的一条：路由标签必须取路由模板，
// 原始路径里的会话 ID 绝不能进标签。
func TestHTTPRouteLabelIsBounded(t *testing.T) {
	h, done := setupForTest(t)
	defer done()

	const sessionID = "3f2b1c9e7d6a5b4c3d2e1f00"
	do(t, h, "/api/sessions/"+sessionID)

	family, ok := gather(t)["http_server_request_duration_seconds"]
	if !ok {
		t.Fatal("http_server_request_duration_seconds 未导出：HTTP 埋点没生效")
	}

	var matched bool
	for _, m := range family.GetMetric() {
		route, _ := labelValue(m, "http_route")
		if route == "/api/sessions/{id}" {
			matched = true
		}
		// 任何序列都不许出现原始 ID。
		for _, l := range m.GetLabel() {
			if strings.Contains(l.GetValue(), sessionID) {
				t.Fatalf("原始路径进入了标签 %s=%q：高基数", l.GetName(), l.GetValue())
			}
		}
	}
	if !matched {
		t.Errorf("http_route 未取到路由模板；实际标签：%s", dumpLabels(family))
	}
}

// TestHTTPMetricLabelsAreCompressed 确认常量标签被 view 压掉。
func TestHTTPMetricLabelsAreCompressed(t *testing.T) {
	h, done := setupForTest(t)
	defer done()
	do(t, h, "/api/sessions/abc")

	family := gather(t)["http_server_request_duration_seconds"]
	if family == nil || len(family.GetMetric()) == 0 {
		t.Fatal("没有 HTTP 指标序列")
	}
	allowed := map[string]bool{
		"http_request_method":         true,
		"http_response_status_code":   true,
		"http_route":                  true,
		"error_type":                  true,
		"otel_scope_name":             false, // WithoutScopeInfo 后不应出现
		"otel_scope_version":          false,
		"target_info":                 false,
	}
	for _, m := range family.GetMetric() {
		for _, l := range m.GetLabel() {
			name := l.GetName()
			if keep, known := allowed[name]; known && !keep {
				t.Errorf("常量标签 %s 未被压掉", name)
			}
		}
	}
	if _, ok := gather(t)["target_info"]; ok {
		t.Error("target_info 仍被导出：应设 WithoutTargetInfo")
	}
}

// TestStreamingRouteIsExcludedFromHTTPMetrics 长连接只结束于客户端断开，不能
// 进请求耗时直方图。
func TestStreamingRouteIsExcludedFromHTTPMetrics(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		Handler().ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /api/sessions/{id}/events", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	shutdown, err := Setup(context.Background())
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()
	h := Middleware(mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/abc/events", nil))

	family := gather(t)["http_server_request_duration_seconds"]
	for _, m := range family.GetMetric() {
		if route, _ := labelValue(m, "http_route"); strings.HasSuffix(route, "/events") {
			t.Errorf("事件流路由 %q 被计入 HTTP 耗时", route)
		}
	}
}

// TestMetricsEndpointIsNotInstrumented 避免抓取出现在它自己提供的指标里。
func TestMetricsEndpointIsNotInstrumented(t *testing.T) {
	h, done := setupForTest(t)
	defer done()

	do(t, h, "/metrics")
	do(t, h, "/api/sessions/abc")

	family := gather(t)["http_server_request_duration_seconds"]
	if family == nil {
		t.Fatal("没有 HTTP 指标序列")
	}
	for _, m := range family.GetMetric() {
		if route, _ := labelValue(m, "http_route"); route == MetricsPath {
			t.Errorf("抓取端点 %s 被自己埋点了", MetricsPath)
		}
	}
}

// TestHistogramBucketsAreSecondsScale 钉住分桶：SDK 默认边界是毫秒尺度的
// 数字，用在秒为单位的直方图上会让分位数不可用。
func TestHistogramBucketsAreSecondsScale(t *testing.T) {
	h, done := setupForTest(t)
	defer done()
	do(t, h, "/api/sessions/abc")

	obs := Obs()
	obs.JobDuration.Record(context.Background(), 42)
	obs.BuildDuration.Record(context.Background(), 0.4)

	families := gather(t)

	http := families["http_server_request_duration_seconds"]
	if http == nil {
		t.Fatal("缺少 HTTP 耗时直方图")
	}
	bounds := bucketBounds(http)
	if len(bounds) == 0 || bounds[0] != "0.005" {
		t.Errorf("HTTP 分桶起点应为 0.005s，实际 %v", headN(bounds, 3))
	}
	if last := bounds[len(bounds)-1]; last != "10" {
		t.Errorf("HTTP 分桶上界应为 10s，实际 %q（SDK 默认是 10000）", last)
	}

	job := families["atoms_job_duration_seconds"]
	if job == nil {
		t.Fatal("缺少 atoms_job_duration_seconds")
	}
	jobBounds := bucketBounds(job)
	if last := jobBounds[len(jobBounds)-1]; last != "1800" {
		t.Errorf("生成类耗时直方图上界应为 1800s，实际 %q", last)
	}
}

// TestNoTenantLabelsAnywhere 是所有指标的一道总闸：任何租户/会话/账号标识
// 都不许作为标签名出现。
func TestNoTenantLabelsAnywhere(t *testing.T) {
	h, done := setupForTest(t)
	defer done()
	do(t, h, "/api/sessions/abc")
	do(t, h, "/api/health")

	forbidden := []string{"session_id", "session", "workspace", "workspace_id", "account", "account_id", "owner", "email", "user_id", "path", "url"}
	for name, family := range gather(t) {
		// 标签名判据只作用于自建指标与 HTTP 指标；go_/process_ 由 client_golang 提供。
		for _, m := range family.GetMetric() {
			for _, l := range m.GetLabel() {
				for _, bad := range forbidden {
					if l.GetName() == bad {
						t.Errorf("%s 出现了租户维度标签 %s=%q", name, bad, l.GetValue())
					}
				}
			}
		}
	}
}

// TestOwnedMetricNames 把自建指标名钉成契约（看板与告警按这些名字写）。
func TestOwnedMetricNames(t *testing.T) {
	_, done := setupForTest(t)
	defer done()

	ctx := context.Background()
	obs := Obs()
	obs.JobEnqueued.Add(ctx, 1, WithString(AttrKind, KindTurn))
	obs.JobEnqueueFail.Add(ctx, 1, WithString(AttrKind, KindTurn), WithString(AttrReason, OutcomeFull))
	obs.JobDelivered.Add(ctx, 2, WithString(AttrKind, KindTurn))
	obs.JobAcknowledged.Add(ctx, 1, WithString(AttrKind, KindTurn))
	obs.JobRedelivered.Add(ctx, 1, WithString(AttrKind, KindTurn), WithString(AttrReason, ReasonHandlerError))
	obs.JobSkipped.Add(ctx, 1, WithString(AttrKind, KindTurn), WithString(AttrReason, ReasonDuplicate))
	obs.JobFailed.Add(ctx, 1, WithString(AttrKind, KindPublish))
	obs.JobRecovered.Add(ctx, 1, WithString(AttrKind, KindTurn))
	obs.JobDuration.Record(ctx, 12.5, WithString(AttrKind, KindTurn))
	obs.TurnFinished.Add(ctx, 1, WithString(AttrStatus, OutcomeReady))
	obs.TurnStageFailed.Add(ctx, 1, WithString(AttrStage, "build"))
	obs.ProviderAttempts.Add(ctx, 1, WithString(AttrProvider, "deepseek"), WithString(AttrOutcome, OutcomeSuccess))
	obs.ProviderDuration.Record(ctx, 30, WithString(AttrProvider, "deepseek"), WithString(AttrOutcome, OutcomeSuccess))
	obs.ProviderFallback.Add(ctx, 1, WithString(AttrProvider, "deepseek"))
	obs.BuildRuns.Add(ctx, 1, WithString(AttrOutcome, OutcomeSuccess))
	obs.BuildDuration.Record(ctx, 0.4, WithString(AttrOutcome, OutcomeSuccess))
	obs.BuildCSSStubs.Add(ctx, 1)
	obs.BuildSelfHealExhausted.Add(ctx, 1)
	obs.SSEClients.Add(ctx, 1, WithString(AttrKind, KindTurn))
	obs.SSESubscribeFail.Add(ctx, 1)
	obs.SSEReplays.Add(ctx, 1)
	obs.EventPublishFail.Add(ctx, 1)
	obs.RecoverySweeps.Add(ctx, 1, WithString(AttrOutcome, OutcomeOK))
	obs.RecoveryRequeued.Add(ctx, 1, WithString(AttrKind, KindTurn))
	obs.BuildGCRuns.Add(ctx, 1, WithString(AttrOutcome, OutcomeOK))
	obs.BuildGCRemoved.Add(ctx, 3)
	obs.SessionsCreated.Add(ctx, 1, WithString(AttrKind, KindContest))
	obs.Publishes.Add(ctx, 1, WithString(AttrOutcome, OutcomeSuccess))
	obs.PayOrders.Add(ctx, 1, WithString(AttrOutcome, OutcomeOK))
	obs.AuthRateLimited.Add(ctx, 1, WithString(AttrScope, "ip"))
	obs.ArtifactOps.Add(ctx, 1, WithString(AttrOp, "put"), WithString(AttrOutcome, OutcomeError))

	families := gather(t)
	want := []string{
		"atoms_job_enqueued_total",
		"atoms_job_enqueue_failed_total",
		"atoms_job_delivered_total",
		"atoms_job_acknowledged_total",
		"atoms_job_redelivered_total",
		"atoms_job_skipped_total",
		"atoms_job_failed_total",
		"atoms_job_recovered_total",
		"atoms_job_duration_seconds",
		"atoms_turn_finished_total",
		"atoms_turn_stage_failed_total",
		"atoms_provider_attempts_total",
		"atoms_provider_duration_seconds",
		"atoms_provider_fallback_total",
		"atoms_build_runs_total",
		"atoms_build_duration_seconds",
		"atoms_build_css_stubs_total",
		"atoms_build_selfheal_exhausted_total",
		"atoms_sse_clients_active",
		"atoms_sse_subscribe_failed_total",
		"atoms_sse_replays_total",
		"atoms_event_publish_failed_total",
		"atoms_recovery_sweeps_total",
		"atoms_recovery_requeued_total",
		"atoms_buildgc_runs_total",
		"atoms_buildgc_removed_total",
		"atoms_sessions_created_total",
		"atoms_publishes_total",
		"atoms_pay_orders_total",
		"atoms_auth_rate_limited_total",
		"atoms_artifact_operations_total",
		"go_goroutines",
		"process_open_fds",
	}
	for _, name := range want {
		if _, ok := families[name]; !ok {
			t.Errorf("缺少指标 %s", name)
		}
	}
}

// TestQueueDepthObservable 确认积压是抓取时拉取、且查不到时不上报 0。
func TestQueueDepthObservable(t *testing.T) {
	_, done := setupForTest(t)
	defer done()

	depth := QueueDepth{Lag: 7, Pending: 3, Depth: 11, Capacity: 64}
	unavailable := false
	if err := RegisterQueueDepth("jobs", func(context.Context) (QueueDepth, error) {
		if unavailable {
			return QueueDepth{}, context.DeadlineExceeded
		}
		return depth, nil
	}); err != nil {
		t.Fatalf("RegisterQueueDepth: %v", err)
	}

	families := gather(t)
	lag := families["atoms_job_queue_lag"]
	if lag == nil || len(lag.GetMetric()) == 0 {
		t.Fatal("缺少 atoms_job_queue_lag")
	}
	if v := labelValue2(t, lag.GetMetric()[0], "queue"); v != "jobs" {
		t.Errorf("queue 标签 = %q，期望 jobs", v)
	}
	if got := lag.GetMetric()[0].GetGauge().GetValue(); got != 7 {
		t.Errorf("lag = %v，期望 7", got)
	}

	// 依赖不可用时不上报：宁可缺数据，也不能把 0 当成真实积压。
	unavailable = true
	families = gather(t)
	if lag := families["atoms_job_queue_lag"]; lag != nil && len(lag.GetMetric()) > 0 {
		t.Error("依赖不可用时仍然上报了 lag")
	}
}

func labelValue2(t *testing.T, m *dto.Metric, name string) string {
	t.Helper()
	v, _ := labelValue(m, name)
	return v
}

func bucketBounds(family *dto.MetricFamily) []string {
	var bounds []string
	for _, m := range family.GetMetric() {
		for _, b := range m.GetHistogram().GetBucket() {
			bounds = append(bounds, strconv.FormatFloat(b.GetUpperBound(), 'f', -1, 64))
		}
		break
	}
	return bounds
}

func headN(s []string, n int) []string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

func dumpLabels(family *dto.MetricFamily) string {
	var sb strings.Builder
	for _, m := range family.GetMetric() {
		for _, l := range m.GetLabel() {
			sb.WriteString(l.GetName())
			sb.WriteString("=")
			sb.WriteString(l.GetValue())
			sb.WriteString(" ")
		}
		sb.WriteString("| ")
	}
	return sb.String()
}
