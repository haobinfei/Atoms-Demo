package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
)

// fakeChatServer returns queued chat-completion bodies in order.
func fakeChatServer(t *testing.T, bodies ...string) *httptest.Server {
	t.Helper()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = w.Write([]byte(`{"data":[{"id":"gemma-test"}]}`))
			return
		}
		if calls >= len(bodies) {
			calls = len(bodies) - 1
		}
		body := bodies[calls]
		calls++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%s}}]}`, jsonString(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestDeepSeekParsesFileSetJSON(t *testing.T) {
	reply := `{"files":{"index.html":"<html></html>","src/App.tsx":"export default function A(){return <div/>}"}}`
	srv := fakeChatServer(t, reply)
	p := provider.NewDeepSeek(srv.URL+"/v1", "key", "test-model")

	files, err := p.Generate(context.Background(), "咖啡店网站", func(string, string) {})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if files["index.html"] != "<html></html>" {
		t.Fatalf("unexpected files: %v", files)
	}
}

func TestDeepSeekParsesFencedJSON(t *testing.T) {
	reply := "```json\n{\"files\":{\"a.tsx\":\"export default function A(){return <div/>}\"}}\n```"
	srv := fakeChatServer(t, reply)
	p := provider.NewDeepSeek(srv.URL+"/v1", "key", "m")

	files, err := p.Generate(context.Background(), "x", func(string, string) {})
	if err != nil {
		t.Fatalf("fenced JSON not parsed: %v", err)
	}
	if files["a.tsx"] == "" {
		t.Fatal("missing file")
	}
}

func TestChainRetriesDeepSeekOnceThenSucceeds(t *testing.T) {
	srv := fakeChatServer(t,
		"sorry, not json at all",
		`{"files":{"src/App.tsx":"export default function A(){return <div/>}"}}`)
	ds := provider.NewDeepSeek(srv.URL+"/v1", "key", "m")
	rec := &emitRecorder{}

	files, err := (provider.Chain{Providers: []provider.Provider{ds}}).Generate(context.Background(), "x", rec.emit)
	if err != nil {
		t.Fatalf("chain did not rescue with retry: %v", err)
	}
	if files["src/App.tsx"] == "" {
		t.Fatal("missing file")
	}
}

func TestLocalProviderRejectsReasoningOnlyOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":"thinking hard"},"finish_reason":"length"}]}`)
	}))
	t.Cleanup(srv.Close)
	p := provider.NewLocal(srv.URL+"/v1", "gemma-test")

	_, err := p.Generate(context.Background(), "x", func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), "reasoning") {
		t.Fatalf("want reasoning-budget error, got: %v", err)
	}
}

func TestLocalProviderResolvesModelFromServer(t *testing.T) {
	srv := fakeChatServer(t, `{"files":{"a.tsx":"export default function A(){return <div/>}"}}`)
	p := provider.NewLocal(srv.URL+"/v1", "")
	if err := p.ResolveModel(context.Background()); err != nil {
		t.Fatalf("ResolveModel: %v", err)
	}
	if p.Name() == "" {
		t.Fatal("no name")
	}
}

func TestTemplateGeneratesValidAppsForAllCategories(t *testing.T) {
	cases := []struct {
		prompt string
		marker string
	}{
		{"一个待办清单应用", "localStorage"},
		{"一个数据运营分析仪表盘", "canvas"},
		{"一家咖啡店的电商网站", "购物车"},
		{"一个设计师的作品集网站", "精选作品"},
		{"一家奶茶店的主页", "招牌菜单"},
		{"一个技术博客", "最新文章"},
		{"一个 SaaS 产品官网", "核心功能"},
	}
	p := provider.NewTemplate()
	for _, tc := range cases {
		files, err := p.Generate(context.Background(), tc.prompt, func(string, string) {})
		if err != nil {
			t.Fatalf("%s: %v", tc.prompt, err)
		}
		if err := provider.Validate(files); err != nil {
			t.Fatalf("%s produced invalid files: %v", tc.prompt, err)
		}
		all := ""
		for _, c := range files {
			all += c
		}
		if !strings.Contains(all, tc.marker) {
			t.Fatalf("%s: marker %q not found", tc.prompt, tc.marker)
		}
	}
}

func TestAnalyzeExtractsNameAndAccent(t *testing.T) {
	spec := provider.Analyze("帮我做一个叫「蓝湖」的数据看板，用蓝色主题")
	if spec.Category != "dashboard" {
		t.Fatalf("category = %s, want dashboard", spec.Category)
	}
	if spec.Name != "蓝湖" {
		t.Fatalf("name = %s, want 蓝湖", spec.Name)
	}
	if spec.Accent != "#3b82f6" {
		t.Fatalf("accent = %s", spec.Accent)
	}
}
