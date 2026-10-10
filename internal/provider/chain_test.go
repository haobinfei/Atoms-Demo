package provider_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
)

type scriptedProvider struct {
	name    string
	results []providerResult
	calls   int
}

type providerResult struct {
	files provider.FileSet
	err   error
}

func (p *scriptedProvider) Name() string { return p.name }

func (p *scriptedProvider) Generate(_ context.Context, _ string, _ func(string, string)) (provider.FileSet, error) {
	i := p.calls
	p.calls++
	if i >= len(p.results) {
		return nil, errors.New("exhausted")
	}
	return p.results[i].files, p.results[i].err
}

type emitRecorder struct {
	stages []string
}

func (r *emitRecorder) emit(stage, detail string) { r.stages = append(r.stages, stage+":"+detail) }

func TestChainDegradesToFallback(t *testing.T) {
	broken := &scriptedProvider{name: "broken", results: []providerResult{
		{err: errors.New("boom")}, {err: errors.New("boom")},
	}}
	ok := &scriptedProvider{name: "ok", results: []providerResult{{files: provider.FileSet{"index.html": "<html/>"}}}}
	rec := &emitRecorder{}

	files, err := (provider.Chain{Providers: []provider.Provider{broken, ok}}).Generate(context.Background(), "x", rec.emit)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if files["index.html"] != "<html/>" {
		t.Fatalf("files from wrong provider: %v", files)
	}
	// broken gets its single retry, then a degradation is announced.
	if broken.calls != 2 {
		t.Fatalf("broken called %d times, want 2 (1 retry)", broken.calls)
	}
	joined := strings.Join(rec.stages, "\n")
	if !strings.Contains(joined, "degrade") || !strings.Contains(joined, "ok") {
		t.Fatalf("degradation not announced: %v", rec.stages)
	}
}

func TestChainRetryRescuesFlakyProvider(t *testing.T) {
	flaky := &scriptedProvider{name: "flaky", results: []providerResult{
		{err: errors.New("garbage json")},
		{files: provider.FileSet{"a.tsx": "export default function A(){return <div/>}"}},
	}}
	rec := &emitRecorder{}
	files, err := (provider.Chain{Providers: []provider.Provider{flaky}}).Generate(context.Background(), "x", rec.emit)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if files["a.tsx"] == "" {
		t.Fatal("expected rescued files")
	}
	if flaky.calls != 2 {
		t.Fatalf("calls = %d, want 2", flaky.calls)
	}
}

func TestChainFailsWhenAllExhausted(t *testing.T) {
	broken := &scriptedProvider{name: "broken", results: []providerResult{{err: errors.New("x")}}}
	_, err := (provider.Chain{Providers: []provider.Provider{broken}}).Generate(context.Background(), "x", (&emitRecorder{}).emit)
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
}

func TestValidateRejectsBadImportsAndPaths(t *testing.T) {
	badImport := provider.FileSet{"src/App.tsx": "import _ from 'lodash'\nexport default function A(){return <div/>}"}
	if err := provider.Validate(badImport); err == nil {
		t.Fatal("disallowed import accepted")
	}
	traversal := provider.FileSet{"../etc/passwd": "x"}
	if err := provider.Validate(traversal); err == nil {
		t.Fatal("path traversal accepted")
	}
	good := provider.FileSet{"src/App.tsx": "import { useState } from 'react'\nexport default function A(){return <div/>}"}
	if err := provider.Validate(good); err != nil {
		t.Fatalf("valid file set rejected: %v", err)
	}
}
