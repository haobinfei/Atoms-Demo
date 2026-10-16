// Chain tries the configured model providers in order. A provider that
// errors or fails file validation (after one retry) is skipped and the
// degradation is announced through the emit callback. There is no template
// provider at the end; exhausting the chain returns an explicit error.
package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/observability"
)

type Chain struct {
	Providers []Provider
}

// Members lists the provider names in chain order; it works on any
// Provider, not just Chain, so callers can inspect what was built.
func Members(p Provider) []string {
	if c, ok := p.(Chain); ok {
		names := make([]string, 0, len(c.Providers))
		for _, prov := range c.Providers {
			names = append(names, prov.Name())
		}
		return names
	}
	return []string{p.Name()}
}

func (c Chain) Name() string { return "chain" }

func (c Chain) Generate(ctx context.Context, prompt string, emit func(stage string, detail string)) (FileSet, error) {
	if len(c.Providers) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}
	obs := observability.Obs()
	var lastErr error
	for i, p := range c.Providers {
		files, err := generateWithRetry(ctx, p, prompt, emit)
		if err == nil {
			return files, nil
		}
		lastErr = err
		if i+1 < len(c.Providers) {
			// 降级次数是「模型链在减员」的直接证据：它不涨说明主模型健康。
			obs.ProviderFallback.Add(ctx, 1, providerAttr(p.Name()))
			emit("degrade", fmt.Sprintf("%s 生成失败（%s），降级到 %s", p.Name(), truncate(err.Error(), 80), c.Providers[i+1].Name()))
		}
	}
	return nil, fmt.Errorf("all providers failed: %w", lastErr)
}

// generateWithRetry gives each provider one retry on validation/parse
// failure before the chain moves on (file-level single retry).
func generateWithRetry(ctx context.Context, p Provider, prompt string, emit func(stage string, detail string)) (FileSet, error) {
	obs := observability.Obs()
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			emit("degrade", fmt.Sprintf("重试 %s（第 %d 次）", p.Name(), attempt+1))
		}
		start := time.Now()
		files, err := p.Generate(ctx, prompt, emit)
		outcome := observability.OutcomeSuccess
		switch {
		case err != nil:
			outcome = observability.OutcomeError
		default:
			if verr := Validate(files); verr != nil {
				// 调用成功但产物不可用：这是「模型幻觉/漏文件」，与网络或配额
				// 错误分开计数才有排障价值。
				outcome = observability.OutcomeInvalid
				err = verr
			}
		}
		obs.ProviderAttempts.Add(ctx, 1, providerOutcome(p.Name(), outcome))
		obs.ProviderDuration.Record(ctx, time.Since(start).Seconds(), providerOutcome(p.Name(), outcome))
		if outcome == observability.OutcomeSuccess {
			return files, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// providerAttr labels a 生成引擎 (Provider) metric with its name.
func providerAttr(name string) observability.MeasurementOption {
	return observability.WithString(observability.AttrProvider, name)
}

// providerOutcome labels an attempt with the provider and a bounded outcome.
func providerOutcome(name, outcome string) observability.MeasurementOption {
	return observability.Attrs(
		observability.AttrProvider.String(name),
		observability.AttrOutcome.String(outcome),
	)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
