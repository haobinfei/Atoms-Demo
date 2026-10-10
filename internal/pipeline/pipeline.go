// Package pipeline orchestrates the AI 团队 through a single 回合 (Turn):
// Mike plans, Emma specifies, Bob designs the architecture, Alex generates
// code, Sarah adds metadata. Every step appends to the session's 事件流.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/builder"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

// Broker fans session events out to connected SSE clients.
// All three methods are safe for concurrent use: Publish runs on pipeline
// goroutines, Subscribe/Unsubscribe on SSE handler goroutines.
type Broker struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan session.Event]struct{}
}

func NewBroker() *Broker {
	return &Broker{subscribers: map[string]map[chan session.Event]struct{}{}}
}

func (b *Broker) Subscribe(id string) chan session.Event {
	ch := make(chan session.Event, 16)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subscribers[id] == nil {
		b.subscribers[id] = map[chan session.Event]struct{}{}
	}
	b.subscribers[id][ch] = struct{}{}
	return ch
}

func (b *Broker) Unsubscribe(id string, ch chan session.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subscribers[id], ch)
	if len(b.subscribers[id]) == 0 {
		delete(b.subscribers, id)
	}
	close(ch)
}

func (b *Broker) Publish(id string, ev session.Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subscribers[id] {
		select {
		case ch <- ev:
		default: // slow client: drop, it can resume via ?after=
		}
	}
}

// Pipeline runs one turn for a session.
type Pipeline struct {
	Store  *session.Store
	Broker *Broker
	Prov   provider.Provider
	// Builder compiles the written files into a previewable dist; nil skips
	// building (kept optional so provider-only tests stay light).
	Builder *builder.Builder
	// StepDelay keeps the timeline human-readable in demos; tests may shrink it.
	StepDelay time.Duration
}

func (p *Pipeline) emit(id string, ev session.Event) {
	appended, err := p.Store.AppendEvent(id, ev)
	if err != nil {
		slog.Error("append event", "session", id, "type", ev.Type, "err", err)
		return
	}
	p.Broker.Publish(id, appended)
}

// EmitUserMessage puts the user's utterance onto the 事件流 so the
// timeline shows it as a bubble before the 智能体 team starts the turn.
func (p *Pipeline) EmitUserMessage(id string, msg session.Message) {
	p.emit(id, session.Event{Type: "message", Payload: map[string]any{
		"role":    msg.Role,
		"content": msg.Content,
	}})
}

// effectivePrompt folds the full 消息历史 into the prompt the 生成引擎
// sees. Turn 1 is the bare original idea; later turns re-state the original
// requirement plus every follow-up, so a full regeneration still honors
// both the initial intent and the newest iteration request.
func effectivePrompt(sess *session.Session) string {
	var user []string
	for _, m := range sess.Messages {
		if m.Role == "user" {
			user = append(user, m.Content)
		}
	}
	if len(user) <= 1 {
		return sess.Prompt
	}
	var b strings.Builder
	fmt.Fprintf(&b, "原始需求：%s\n\n请在保持原始需求不变的前提下，按以下迭代要求重新生成完整应用：", user[0])
	for i, u := range user[1:] {
		fmt.Fprintf(&b, "\n%d. %s", i+1, u)
	}
	return b.String()
}

// Run executes the full agent sequence for the session's current prompt.
func (p *Pipeline) Run(ctx context.Context, id string) {
	fail := func(err error) {
		p.emit(id, session.Event{Type: "error", Payload: map[string]any{"message": err.Error()}})
	}
	files, err := p.run(ctx, id)
	status := "ready"
	if err != nil {
		status = "failed"
		fail(err)
	}
	if files != nil {
		if err := p.writeFiles(id, files); err != nil {
			status = "failed"
			fail(err)
		} else if p.Builder != nil {
			appDir := filepath.Join(p.Store.Workspace(id), "app")
			distDir := filepath.Join(p.Store.Workspace(id), "dist")
			if err := p.Builder.Build(appDir, distDir); err != nil {
				// Drop any stale dist so /app never serves an outdated build
				// while the session is failed.
				_ = os.RemoveAll(distDir)
				status = "failed"
				fail(err)
			}
		}
	}
	p.setStatus(id, status)
	p.emit(id, session.Event{Type: "done", Agent: "sarah", Payload: map[string]any{
		"status": status,
		"detail": "补充 SEO 元信息：title、description、og 标签",
	}})
}

func (p *Pipeline) run(ctx context.Context, id string) (provider.FileSet, error) {
	sess, err := p.Store.Load(id)
	if err != nil {
		return nil, err
	}
	step := func(agent, typ, detail string) bool {
		if p.StepDelay > 0 {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(p.StepDelay):
			}
		}
		payload := map[string]any{"detail": detail}
		p.emit(id, session.Event{Type: typ, Agent: agent, Payload: payload})
		return true
	}

	if !step("mike", "plan", "拆解需求并制定执行计划") {
		return nil, ctx.Err()
	}
	if !step("emma", "spec", fmt.Sprintf("为「%s」整理产品规格", effectivePrompt(sess))) {
		return nil, ctx.Err()
	}
	if !step("bob", "architecture", "设计系统结构：React + TypeScript，组件化组织") {
		return nil, ctx.Err()
	}

	var files provider.FileSet
	files, err = p.Prov.Generate(ctx, effectivePrompt(sess), func(stage, detail string) {
		if stage == "degrade" {
			p.emit(id, session.Event{Type: "agent", Agent: "mike", Payload: map[string]any{"detail": detail}})
		}
	})
	if err != nil {
		return nil, err
	}
	// The pipeline alone announces file writes, uniformly for every
	// 生成引擎 (chain, single LLM provider, or test fake).
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		p.emit(id, session.Event{Type: "file", Agent: "alex", Payload: map[string]any{"path": path}})
	}
	return files, nil
}

func (p *Pipeline) writeFiles(id string, files provider.FileSet) error {
	// Defense in depth: files from a directly-injected Prov (tests, future
	// bare providers) bypass the chain's validation — enforce it at the
	// write boundary too.
	if err := provider.Validate(files); err != nil {
		return fmt.Errorf("invalid file set: %w", err)
	}
	appDir := filepath.Join(p.Store.Workspace(id), "app")
	for path, content := range files {
		full := filepath.Join(appDir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (p *Pipeline) setStatus(id, status string) {
	sess, err := p.Store.Load(id)
	if err != nil {
		slog.Error("load session for status update", "session", id, "err", err)
		return
	}
	sess.Status = status
	if err := p.Store.Save(sess); err != nil {
		slog.Error("save session status", "session", id, "status", status, "err", err)
	}
}
