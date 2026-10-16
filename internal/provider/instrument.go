package provider

import (
	"context"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/observability"
)

// Instrumented 包装一个 生成引擎 (Provider)，把每次调用计入指标。
//
// 为什么是包装 Provider 而不是埋在 Chain 里：调用生成引擎一共有三条路——
// 回合走 provider.Chain、竞赛里每个参赛者被 runContestant 直接调用、评审打分
// 再单独调一次模型。埋在 Chain 里只覆盖第一条，这是 v0.6.1 的实测教训：跑一次
// 竞赛，atoms_provider_* 一条都没有。包装 Provider 实例则三条路一次覆盖，模板
// provider（测试与 e2e 夹具）同样被覆盖。
//
// 它不改变 Provider 的契约：调用成功但产物不合规时仍然原样返回 FileSet 与 nil
// error，由调用方自己校验——指标只观察，不改语义。
type Instrumented struct {
	name  string
	inner Provider
}

// Instrument 包装 p。重复包装是安全的（已包装则原样返回）。
func Instrument(p Provider) Provider {
	if p == nil {
		return nil
	}
	if _, wrapped := p.(Instrumented); wrapped {
		return p
	}
	return Instrumented{name: p.Name(), inner: p}
}

// InstrumentAll wraps every member of a chain.
func InstrumentAll(providers []Provider) []Provider {
	out := make([]Provider, 0, len(providers))
	for _, p := range providers {
		out = append(out, Instrument(p))
	}
	return out
}

func (i Instrumented) Name() string { return i.name }

// Generate 透传调用，并把结果分成三类：调用失败（网络/配额/解析）与
// 「调用成功但产物不可用」（模型幻觉、漏文件）。两者处置方式完全不同，必须分开。
func (i Instrumented) Generate(ctx context.Context, prompt string, emit func(stage string, detail string)) (FileSet, error) {
	obs := observability.Obs()
	start := time.Now()
	files, err := i.inner.Generate(ctx, prompt, emit)

	outcome := observability.OutcomeSuccess
	if err != nil {
		outcome = observability.OutcomeError
	} else if verr := Validate(files); verr != nil {
		outcome = observability.OutcomeInvalid
	}
	obs.ProviderAttempts.Add(ctx, 1, observability.Attrs(
		observability.AttrProvider.String(i.name),
		observability.AttrOutcome.String(outcome),
	))
	obs.ProviderDuration.Record(ctx, time.Since(start).Seconds(), observability.Attrs(
		observability.AttrProvider.String(i.name),
		observability.AttrOutcome.String(outcome),
	))
	return files, err
}