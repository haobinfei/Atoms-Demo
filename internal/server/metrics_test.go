package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/identity"
	"github.com/haobinfei/Atoms-Demo/internal/observability"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
)

// TestMetricsEndpointIsScrapable 是装配层面的回归测试：/metrics 必须能在
// 启用身份中间件的情况下被匿名抓取（Prometheus 不带会话 Cookie），否则整条
// 采集链路在线上是断的，而单看代码看不出来。
func TestMetricsEndpointIsScrapable(t *testing.T) {
	shutdown, err := observability.Setup(context.Background())
	if err != nil {
		t.Fatalf("observability.Setup: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	// Identity 非 nil 才会装上 identityMiddleware。请求不带 Cookie，所以中间件
	// 不会去查库，nil pool 不会被触到。
	srv := server.New(server.Config{
		WorkspaceDir: t.TempDir(),
		Identity:     identity.New(nil),
		Prov:         provider.FakeProvider{},
	})
	ts := httptest.NewServer(srv.Handler())
	defer func() {
		ts.Close()
		_ = srv.Close()
	}()

	// 先打一个被埋点的请求（/api/health 是公开路径），否则耗时直方图没有样本、
	// 也就不会有序列。
	healthResp, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	_ = healthResp.Body.Close()

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics: status %d，期望 200（被中间件拦下了？）", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	body := string(raw)

	// 三组指标分别证明：运行时采集、HTTP 埋点、以及队列 observable 都接上了。
	for _, want := range []string{
		"go_goroutines",
		"process_open_fds",
		"http_server_request_duration_seconds",
		"atoms_job_queue_lag",
		"atoms_job_queue_capacity",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics 里缺少 %s", want)
		}
	}

	// 抓取自己不该留下痕迹。
	if strings.Contains(body, `http_route="/metrics"`) {
		t.Error("抓取端点出现在自己的 http_route 标签里")
	}
	// 常数标签应被压掉。
	if strings.Contains(body, "otel_scope_name") {
		t.Error("otel_scope_* 未被压掉")
	}

	// 指标名是看板与告警的契约，-v 时把清单打出来便于逐条核对。
	if testing.Verbose() {
		t.Logf("暴露的指标名：\n%s", metricNames(body))
	}
}

// metricNames 抽出 /metrics 输出里的指标名（去掉注释与标签）。
func metricNames(body string) string {
	seen := map[string]bool{}
	var names []string
	for _, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, _, _ := strings.Cut(line, "{")
		name, _, _ = strings.Cut(name, " ")
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return strings.Join(names, "\n")
}

// TestQueueDepthReflectsBacklog 确认 server 把队列 observable 真的注册上了，
// 而且值来自队列本身而不是一个写死的 0。
func TestQueueDepthReflectsBacklog(t *testing.T) {
	shutdown, err := observability.Setup(context.Background())
	if err != nil {
		t.Fatalf("observability.Setup: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	srv := server.New(server.Config{WorkspaceDir: t.TempDir(), DevAuth: true, Prov: provider.FakeProvider{}})
	ts := httptest.NewServer(srv.Handler())
	defer func() {
		ts.Close()
		_ = srv.Close()
	}()

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)

	// MemoryQueue 以 QueueWorkers×4 为容量（默认 3 个 Worker）。
	if !strings.Contains(body, `atoms_job_queue_capacity{queue="jobs"} 12`) {
		t.Errorf("队列容量未按 Worker×4 上报；实际：\n%s", grepLines(body, "atoms_job_queue_capacity"))
	}
	if !strings.Contains(body, `atoms_job_queue_lag{queue="jobs"} 0`) {
		t.Errorf("空队列的 lag 应为 0；实际：\n%s", grepLines(body, "atoms_job_queue_lag"))
	}
}

func grepLines(body, needle string) string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, needle) {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
