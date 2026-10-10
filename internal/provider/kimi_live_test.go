package provider_test

import (
	"context"
	"os"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
)

// Manual live check against the real Kimi Code endpoint; run with:
//   KIMI_LIVE=1 go test ./internal/provider/ -run TestKimiLive -v
func TestKimiLive(t *testing.T) {
	if os.Getenv("KIMI_LIVE") == "" {
		t.Skip("KIMI_LIVE not set")
	}
	p := provider.NewKimi("", os.Getenv("KIMI_API_KEY"), "")
	files, err := p.Generate(context.Background(), "一个番茄钟待办应用", func(string, string) {})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	t.Logf("files: %v", func() []string { ks := make([]string, 0, len(files)); for k := range files { ks = append(ks, k) }; return ks }())
}
