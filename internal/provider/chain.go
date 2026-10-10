// Chain implements the ADR-0002 兜底链: providers are tried in order; a
// provider that errors or fails file validation (after one retry) is
// skipped and the degradation is announced through the emit callback.
package provider

import (
	"context"
	"fmt"
	"sort"
)

type Chain struct {
	Providers []Provider
}

func (c Chain) Name() string { return "chain" }

func (c Chain) Generate(ctx context.Context, prompt string, emit func(stage string, detail string)) (FileSet, error) {
	var lastErr error
	for i, p := range c.Providers {
		files, err := generateWithRetry(ctx, p, prompt, emit)
		if err == nil {
			return files, nil
		}
		lastErr = err
		if i+1 < len(c.Providers) {
			emit("degrade", fmt.Sprintf("%s 生成失败（%s），降级到 %s", p.Name(), truncate(err.Error(), 80), c.Providers[i+1].Name()))
		}
	}
	return nil, fmt.Errorf("all providers failed: %w", lastErr)
}

// generateWithRetry gives each provider one retry on validation/parse
// failure before the chain moves on (file-level single retry).
func generateWithRetry(ctx context.Context, p Provider, prompt string, emit func(stage string, detail string)) (FileSet, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			emit("degrade", fmt.Sprintf("重试 %s（第 %d 次）", p.Name(), attempt+1))
		}
		files, err := p.Generate(ctx, prompt, emit)
		if err != nil {
			lastErr = err
			continue
		}
		if err := Validate(files); err != nil {
			lastErr = err
			continue
		}
		// LLM providers return the whole set at once; announce each file so
		// the timeline shows writes regardless of which engine produced them.
		paths := make([]string, 0, len(files))
		for path := range files {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			emit("file", path)
		}
		return files, nil
	}
	return nil, lastErr
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
