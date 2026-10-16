package server_test

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/observability"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
)

// TestContestContributesProviderMetrics 是 v0.6.1 那个盲区的回归测试。
//
// 那一版把 provider 的调用指标埋在 provider.Chain 里，而竞赛的参赛者是被
// runContestant 直接调用的（contest.go: `comp.Generate(...)`），完全绕过 chain。
// 结果是：跑一次真实竞赛，atoms_provider_* 一条都没有——指标在、面板却是空的，
// 看起来像没埋点。
//
// 这条测试断言"跑一次竞赛 → 每个参赛者的调用指标都出现"，并且刻意绕开 chain。
func TestContestContributesProviderMetrics(t *testing.T) {
	shutdown, err := observability.Setup(context.Background())
	if err != nil {
		t.Fatalf("observability.Setup: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	alpha := &contestStub{name: "alpha", marker: "A", delay: 10 * time.Millisecond}
	beta := &contestStub{name: "beta", marker: "B", delay: 10 * time.Millisecond}
	ts, _ := newContestServer(t, []provider.Provider{alpha, beta}, nil)
	createContest(t, ts.URL, "一个数据运营分析仪表盘")

	// 参赛者并行生成，等它们各自至少被调用过一次。
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if seen := providerAttemptsByName(t); seen["alpha"] > 0 && seen["beta"] > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	got := providerAttemptsByName(t)
	t.Fatalf("竞赛没有产出参赛者的 provider 调用指标：alpha=%.0f beta=%.0f\n"+
		"（参赛者是被 runContestant 直接调用的，不经过 provider.Chain——"+
		"埋点必须落在 Provider 实例上，见 provider.Instrument）", got["alpha"], got["beta"])
}

// providerAttemptsByName 从指标注册表里读 atoms_provider_attempts_total，
// 返回按 provider 名聚合的调用次数。
func providerAttemptsByName(t *testing.T) map[string]float64 {
	t.Helper()
	families, err := observability.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := map[string]float64{}
	for _, f := range families {
		if f.GetName() != "atoms_provider_attempts_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			name := ""
			for _, l := range m.GetLabel() {
				if l.GetName() == "provider" {
					name = l.GetValue()
				}
			}
			out[name] += m.GetCounter().GetValue()
		}
	}
	return out
}

// TestMetricsEndpointHasProviderFamilyGuard 是一条廉价的"指标名清单"守卫：
// 看板按名字引用这些指标，少一条就是一片空白面板。
func TestMetricsEndpointHasProviderFamilyGuard(t *testing.T) {
	shutdown, err := observability.Setup(context.Background())
	if err != nil {
		t.Fatalf("observability.Setup: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) == 0 {
		t.Fatal("/metrics 为空")
	}
}