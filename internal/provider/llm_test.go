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
		`{"files":{"index.html":"<html></html>","src/main.tsx":"import App from './App'","src/App.tsx":"export default function A(){return <div/>}"}}`)
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

func TestChainFromEnvProbesOutUnreachableLocal(t *testing.T) {
	// Port 1 on localhost is guaranteed closed; the probe must fail fast.
	chain := provider.ChainFromEnv(context.Background(), provider.EnvConfig{
		LocalBaseURL: "http://127.0.0.1:1/v1",
	})
	names := provider.Members(chain)
	if len(names) != 0 {
		t.Fatalf("unreachable chain should have no providers, got %v", names)
	}
}

func TestKimiProviderGeneratesFileSet(t *testing.T) {
	srv := fakeChatServer(t, `{"files":{"a.tsx":"export default function A(){return <div/>}"}}`)
	p := provider.NewKimi(srv.URL+"/v1", "sk-test", "")
	files, err := p.Generate(context.Background(), "一个待办应用", func(string, string) {})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, ok := files["a.tsx"]; !ok {
		t.Fatalf("files missing a.tsx: %v", files)
	}
	if p.Name() != "kimi" {
		t.Fatalf("name = %q, want kimi", p.Name())
	}
}

func TestChainFromEnvIncludesKimiWhenKeyed(t *testing.T) {
	chain := provider.ChainFromEnv(context.Background(), provider.EnvConfig{
		KimiAPIKey:   "sk-test",
		KimiBaseURL:  "http://127.0.0.1:1/v1",
		LocalBaseURL: "http://127.0.0.1:1/v1",
	})
	names := provider.Members(chain)
	if len(names) != 1 || names[0] != "kimi" {
		t.Fatalf("chain should contain only kimi, got %v", names)
	}
}

func TestChainFromEnvDoesNotAddTemplateFallback(t *testing.T) {
	chain := provider.ChainFromEnv(context.Background(), provider.EnvConfig{})
	if names := provider.Members(chain); len(names) != 0 {
		t.Fatalf("empty environment should configure no model, got %v", names)
	}
	_, err := chain.Generate(context.Background(), "一个个人博客", func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), "no providers configured") {
		t.Fatalf("want explicit no-model error, got %v", err)
	}
}

func TestMembersReflectsChainOrder(t *testing.T) {
	chain := provider.Chain{Providers: []provider.Provider{
		provider.NewTemplate(), provider.FakeProvider{},
	}}
	got := provider.Members(chain)
	want := []string{"template", "fake"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("members = %v, want %v", got, want)
	}
	if m := provider.Members(provider.FakeProvider{}); len(m) != 1 || m[0] != "fake" {
		t.Fatalf("single provider members = %v", m)
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

func TestAnalyzeUsesLatestIterationIntent(t *testing.T) {
	prompt := provider.BuildIterationPrompt(
		"一个蓝色主题的个人博客",
		map[string]string{"src/styles.css": "body { color: #3b82f6; }"},
		[]string{"增加三篇文章", "把主题改成赛博朋克，使用霓虹青色"},
	)
	spec := provider.Analyze(prompt)
	if spec.Category != "site" || spec.SiteKind != "blog" {
		t.Fatalf("category = %s/%s, want site/blog", spec.Category, spec.SiteKind)
	}
	if spec.Accent != "#22d3ee" {
		t.Fatalf("accent = %s, want cyber cyan #22d3ee", spec.Accent)
	}
}

func TestBuildIterationPromptPrioritizesLatestRequest(t *testing.T) {
	prompt := provider.BuildIterationPrompt(
		"一个个人博客",
		map[string]string{"src/App.tsx": "const articles = ['旧文章']"},
		[]string{"增加三篇文章", "再增加两篇关于 Go 的文章"},
	)
	for _, want := range []string{
		"【原始需求】",
		"【当前版本文件】",
		"src/App.tsx",
		"【历史迭代要求】",
		"增加三篇文章",
		"【本轮最新要求｜最高优先级，必须执行】",
		"再增加两篇关于 Go 的文章",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("iteration prompt missing %q:\n%s", want, prompt)
		}
	}
	latest := strings.LastIndex(prompt, "【本轮最新要求｜最高优先级，必须执行】")
	if latest < strings.LastIndex(prompt, "【历史迭代要求】") {
		t.Fatalf("latest request must appear after history:\n%s", prompt)
	}
}
