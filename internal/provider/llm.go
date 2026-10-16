// DeepSeekProvider and LocalProvider: two OpenAI-compatible LLM backends
// sharing the same generation prompt and JSON parsing.
package provider

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const systemPrompt = `你是 Atoms Demo 的应用生成引擎。根据用户的想法，生成一个完整可运行的 React 19 + TypeScript 单页应用。

严格只输出一个 JSON 对象，格式：{"files": {"路径": "文件内容", ...}}，不要输出任何其他文字、解释或 markdown 围栏。

硬性约束：
1. 必须包含 index.html（含 <div id="root"> 和指向 /src/main.tsx 的 module script）与 src/main.tsx（从 react-dom/client 创建 root 并渲染 <App />）。
2. 只允许 import react 和 react-dom/client（以及相对路径的本地模块和 .css 文件）。禁止任何其他第三方依赖。
3. 样式用一个 src/styles.css，通过 CSS 变量 --accent 控制主题色，设计要精致现代、响应式。
4. 应用必须是真实可交互的：使用 useState/useEffect，数据存 localStorage，不许是纯静态占位页。
5. 界面文案使用中文（除非用户明确要求其他语言）。
6. 文件数量 4-8 个，代码必须完整，不得截断。
7. 电商类应用（商品、购物车、下单）必须通过平台注入的 SDK 完成支付闭环：import { createOrder, listOrders } from './atomspay'（注意是相对路径 './atomspay'，不是 'atomspay' 或 'atomspay:atomspay'）；结算时调用 createOrder(商品数组) 并跳转到返回的 checkoutUrl，订单列表用 listOrders() 拉取，并用 consumePaidCallback() 处理支付成功回调。该模块只导出 createOrder / listOrders / consumePaidCallback 三个函数，臆造其他函数名或模块名会直接构建失败。禁止自实现任何支付/卡号输入界面。
8. 迭代生成时，【本轮最新要求】是最高优先级指令，必须实际落实到代码、样式、文案或交互中；与原始需求或历史要求冲突时，以本轮最新要求为准。
9. 不得以“保持整体结构”为理由原样返回当前版本；本轮要求涉及的部分必须产生可观察变化，同时输出完整文件集。`

// DeepSeekProvider is the primary 生成引擎 (cloud, fast).
type DeepSeekProvider struct {
	Client *openAIClient
}

func NewDeepSeek(baseURL, apiKey, model string) *DeepSeekProvider {
	if baseURL == "" {
		baseURL = "https://api.deepseek.com/v1"
	}
	if model == "" {
		model = "deepseek-v4-pro"
	}
	return &DeepSeekProvider{Client: &openAIClient{
		baseURL: baseURL, apiKey: apiKey, model: model, stream: true,
		http: defaultHTTPClient(15 * time.Minute),
	}}
}

func (p *DeepSeekProvider) Name() string { return "deepseek" }

func (p *DeepSeekProvider) Generate(ctx context.Context, prompt string, emit func(stage string, detail string)) (FileSet, error) {
	raw, err := p.Client.chat(ctx, []chatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: prompt},
	}, 32000, true)
	if err != nil {
		return nil, err
	}
	files, err := parseFileSet(raw)
	if err != nil {
		return nil, err
	}
	return files, nil
}

// LocalProvider talks to the home-hosted gemma server
// (default http://192.168.1.101:8090/v1). It is a reasoning model: the
// token budget must cover its thinking phase or the content comes back
// empty (ADR-0002).
type LocalProvider struct {
	Client *openAIClient
}

func NewLocal(baseURL, model string) *LocalProvider {
	if baseURL == "" {
		baseURL = "http://192.168.1.101:8090/v1"
	}
	return &LocalProvider{Client: &openAIClient{
		baseURL: baseURL, model: model, stream: true,
		http: defaultHTTPClient(15 * time.Minute),
	}}
}

func (p *LocalProvider) Name() string { return "gemma-local" }

// ResolveModel asks the server for its model list when no model is fixed.
func (p *LocalProvider) ResolveModel(ctx context.Context) error {
	if p.Client.model != "" {
		return nil
	}
	models, err := listModels(ctx, p.Client)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		return fmt.Errorf("no models served")
	}
	p.Client.model = models[0]
	return nil
}

func (p *LocalProvider) Generate(ctx context.Context, prompt string, emit func(stage string, detail string)) (FileSet, error) {
	raw, err := p.Client.chat(ctx, []chatMessage{
		{Role: "system", Content: systemPrompt + "\n\n先思考再输出，但务必把 JSON 正文写完整。"},
		{Role: "user", Content: prompt},
	}, 48000, false)
	if err != nil {
		return nil, err
	}
	files, err := parseFileSet(raw)
	if err != nil {
		return nil, err
	}
	return files, nil
}

// KimiProvider is a third cloud 生成引擎 (Moonshot Kimi, OpenAI-compatible).
// Same JSON-mode contract as DeepSeekProvider; the key/base URL pair may
// also point at a self-hosted gateway that fronts Kimi.
type KimiProvider struct {
	Client *openAIClient
}

func NewKimi(baseURL, apiKey, model string) *KimiProvider {
	if baseURL == "" {
		baseURL = "https://api.kimi.com/coding/v1"
	}
	if model == "" {
		model = "kimi-for-coding"
	}
	// kimi-for-coding only accepts temperature=1.
	one := 1.0
	return &KimiProvider{Client: &openAIClient{
		baseURL: baseURL, apiKey: apiKey, model: model, stream: true,
		http:        defaultHTTPClient(15 * time.Minute),
		temperature: &one,
	}}
}

func (p *KimiProvider) Name() string { return "kimi" }

func (p *KimiProvider) Generate(ctx context.Context, prompt string, emit func(stage string, detail string)) (FileSet, error) {
	raw, err := p.Client.chat(ctx, []chatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: prompt},
	}, 32000, true)
	if err != nil {
		return nil, err
	}
	files, err := parseFileSet(raw)
	if err != nil {
		return nil, err
	}
	return files, nil
}

// ChainFromEnv builds the configured model chain from the environment:
// DeepSeek (if a key is set) → Kimi (if a key is set) → local gemma (if
// reachable at startup). There is deliberately no template fallback: a
// missing or failed model must surface as an error, not as a misleading
// successful generation with fixed content.
func ChainFromEnv(ctx context.Context, cfg EnvConfig) Provider {
	var chain []Provider
	if cfg.DeepSeekAPIKey != "" {
		chain = append(chain, NewDeepSeek(cfg.DeepSeekBaseURL, cfg.DeepSeekAPIKey, cfg.DeepSeekModel))
	}
	if cfg.KimiAPIKey != "" {
		chain = append(chain, NewKimi(cfg.KimiBaseURL, cfg.KimiAPIKey, cfg.KimiModel))
	}
	if cfg.LocalBaseURL != "" {
		local := NewLocal(cfg.LocalBaseURL, cfg.LocalModel)
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := local.ResolveModel(probeCtx)
		cancel()
		if err == nil {
			chain = append(chain, local)
		} else {
			slog.Warn("local llm probe failed, skipping", "base_url", cfg.LocalBaseURL, "err", err)
		}
	}
	// 每个成员都被 Instrumented 包装：回合（经 Chain）、竞赛（runContestant
	// 直接调用成员）、评审打分（scoreWithLLM）三条路因此共用同一套调用指标。
	for i, p := range chain {
		chain[i] = Instrument(p)
	}
	return Chain{Providers: chain}
}

type EnvConfig struct {
	DeepSeekAPIKey  string
	DeepSeekBaseURL string
	DeepSeekModel   string
	KimiAPIKey      string
	KimiBaseURL     string
	KimiModel       string
	LocalBaseURL    string
	LocalModel      string
}
