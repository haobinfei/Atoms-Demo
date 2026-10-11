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
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/builder"
	"github.com/haobinfei/Atoms-Demo/internal/eventstream"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

// Pipeline runs one turn for a session.
type Pipeline struct {
	Store session.Store
	// Bus fans appended events out to SSE clients. MemoryBus keeps
	// single-replica behavior; production wires a FanoutBus over Redis
	// Streams. A nil Bus only degrades live delivery — every event is
	// already persisted, so clients still catch up via ?after= replay.
	Bus  eventstream.Bus
	Prov provider.Provider
	// Builder compiles the written files into a previewable dist; nil skips
	// building (kept optional so provider-only tests stay light).
	Builder *builder.Builder
	// StepDelay keeps the timeline human-readable in demos; tests may shrink it.
	StepDelay time.Duration
}

func (p *Pipeline) emit(owner, id string, ev session.Event) {
	appended, err := p.Store.AppendEvent(owner, id, ev)
	if err != nil {
		slog.Error("append event", "session", id, "type", ev.Type, "err", err)
		return
	}
	if p.Bus == nil {
		return
	}
	// Postgres already holds the event; Redis fan-out is a hot path, so a
	// publish failure must never fail or roll back the turn (ADR-0007).
	if err := p.Bus.Publish(context.Background(), id, appended); err != nil {
		slog.Warn("publish event", "session", id, "seq", appended.Seq, "err", err)
	}
}

// EmitUserMessage puts the user's utterance onto the 事件流 so the
// timeline shows it as a bubble before the 智能体 team starts the turn.
func (p *Pipeline) EmitUserMessage(owner, id string, msg session.Message) {
	p.emit(owner, id, session.Event{Type: "message", Payload: map[string]any{
		"role":    msg.Role,
		"content": msg.Content,
	}})
}

// EmitContestStatus announces one 竞赛 (Competition) stage on the timeline:
// start / a contestant ready / a contestant failed / the winner.
func (p *Pipeline) EmitContestStatus(owner, id, stage, detail string) {
	p.emit(owner, id, session.Event{Type: "contest", Agent: "nina", Payload: map[string]any{
		"stage":  stage,
		"detail": detail,
	}})
}

// EmitError puts a failure notice on the timeline, e.g. when contest
// initialization itself failed.
func (p *Pipeline) EmitError(owner, id, message string) {
	p.emit(owner, id, session.Event{Type: "error", Payload: map[string]any{
		"message": message,
	}})
}

// EmitScore publishes Nina's three-dimension verdict for one contestant.
func (p *Pipeline) EmitScore(owner, id, provider string, scores session.Scores, comment string) {
	p.emit(owner, id, session.Event{Type: "score", Agent: "nina", Payload: map[string]any{
		"provider":      provider,
		"design":        scores.Design,
		"functionality": scores.Functionality,
		"codeQuality":   scores.CodeQuality,
		"comment":       comment,
	}})
}

// EmitPublishDone announces a finished 发布 (Publish): the rebuilt version is
// now live at url. Emitted by the publish Worker, not the HTTP handler.
func (p *Pipeline) EmitPublishDone(owner, id string, version int, url string) {
	p.emit(owner, id, session.Event{Type: "publish", Payload: map[string]any{
		"version": version,
		"url":     url,
		"detail":  fmt.Sprintf("已发布 v%d", version),
	}})
}

// EmitRecovered announces that a crashed 任务 (Job) was picked up by the
// crash-recovery sweeper and is being re-run from scratch. It is emitted by
// the Worker, after the execution lease is claimed, so a redelivered or
// duplicated recovery Job cannot emit it twice.
func (p *Pipeline) EmitRecovered(owner, id, kind string) {
	p.emit(owner, id, session.Event{Type: "recovered", Agent: "system", Payload: map[string]any{
		"kind":   kind,
		"detail": "实例重启，自动恢复重跑",
	}})
}

// EmitContestDone closes the contest: scoring finished, session ready,
// waiting for the user's winner pick.
func (p *Pipeline) EmitContestDone(owner, id string) {
	p.emit(owner, id, session.Event{Type: "done", Agent: "nina", Payload: map[string]any{
		"status": "ready",
		"detail": "评审完成，请点选胜者",
	}})
}

// effectivePrompt folds the full 消息历史 and the current file set into the
// prompt the 生成引擎 sees (ADR-0003). Turn 1 is the bare original idea;
// later turns re-state the original requirement, the files of the current
// version, and every follow-up, so a full regeneration still honors both
// the initial intent and the newest iteration request.
// sess.Prompt and Messages[0] are the same fact by construction (Create
// writes both); later turns rebuild from Messages so the engine never sees
// a bare follow-up out of context.
func effectivePrompt(sess *session.Session, files map[string]string) string {
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
	fmt.Fprintf(&b, "原始需求：%s\n", user[0])
	if len(files) > 0 {
		b.WriteString("\n当前版本文件内容（请在保持整体结构的前提下输出完整的新版本）：")
		paths := make([]string, 0, len(files))
		for path := range files {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			fmt.Fprintf(&b, "\n\n--- %s ---\n%s", path, files[path])
		}
	}
	b.WriteString("\n\n请在保持原始需求不变的前提下，按以下迭代要求重新生成完整应用：")
	for i, u := range user[1:] {
		fmt.Fprintf(&b, "\n%d. %s", i+1, u)
	}
	return b.String()
}

// readAppFiles loads the session's current generated sources (empty on turn
// 1, when the app directory does not exist yet).
func readAppFiles(dir string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// Run executes the full agent sequence for the session's current prompt.
func (p *Pipeline) Run(ctx context.Context, owner, id string) {
	fail := func(err error) {
		p.emit(owner, id, session.Event{Type: "error", Payload: map[string]any{"message": err.Error()}})
	}
	files, err := p.run(ctx, owner, id)
	status := "ready"
	if err != nil {
		status = "failed"
		fail(err)
	}
	if files != nil {
		if err := p.writeFiles(owner, id, files); err != nil {
			status = "failed"
			fail(err)
		} else if p.Builder != nil {
			appDir := filepath.Join(p.Store.Workspace(owner, id), "app")
			distDir := filepath.Join(p.Store.Workspace(owner, id), "dist")
			if err := p.Builder.Build(appDir, distDir); err != nil {
				// Drop any stale dist so /app never serves an outdated build
				// while the session is failed.
				_ = os.RemoveAll(distDir)
				status = "failed"
				fail(err)
			}
		}
	}
	p.setStatus(owner, id, status)
	// The summary lands before "done" so that a client that has seen done
	// can rely on all session writes being complete (tests clean up
	// workspaces on done; a trailing write would race the cleanup).
	p.appendTurnSummary(owner, id, status, files, err)
	p.emit(owner, id, session.Event{Type: "done", Agent: "sarah", Payload: map[string]any{
		"status": status,
		"detail": "补充 SEO 元信息：title、description、og 标签",
	}})
}

// appendTurnSummary records the agent's side of the conversation so the
// persisted 消息历史 carries both user utterances and agent responses.
func (p *Pipeline) appendTurnSummary(owner, id, status string, files provider.FileSet, runErr error) {
	content := fmt.Sprintf("应用已生成（生成引擎：%s，共 %d 个文件）", p.Prov.Name(), len(files))
	if status == "failed" {
		content = "本回合构建失败"
		if runErr != nil {
			content = fmt.Sprintf("%s：%v", content, runErr)
		}
	}
	msg := session.Message{Role: "agent", Agent: "sarah", Content: content}
	if err := p.Store.AppendMessage(owner, id, msg); err != nil {
		slog.Error("append turn summary", "session", id, "err", err)
	}
}

func (p *Pipeline) run(ctx context.Context, owner, id string) (provider.FileSet, error) {
	sess, err := p.Store.Load(owner, id)
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
		p.emit(owner, id, session.Event{Type: typ, Agent: agent, Payload: payload})
		return true
	}

	if !step("mike", "plan", "拆解需求并制定执行计划") {
		return nil, ctx.Err()
	}
	if !step("emma", "spec", fmt.Sprintf("为「%s」整理产品规格", sess.Prompt)) {
		return nil, ctx.Err()
	}
	if !step("bob", "architecture", "设计系统结构：React + TypeScript，组件化组织") {
		return nil, ctx.Err()
	}

	// Current-version files feed the augmented prompt (ADR-0003); missing
	// on turn 1, stale-read races are harmless because regeneration is
	// prompt-driven and the write below lands atomically per file.
	var current map[string]string
	if files, err := readAppFiles(filepath.Join(p.Store.Workspace(owner, id), "app")); err == nil {
		current = files
	}
	var files provider.FileSet
	files, err = p.Prov.Generate(ctx, effectivePrompt(sess, current), func(stage, detail string) {
		if stage == "degrade" {
			p.emit(owner, id, session.Event{Type: "agent", Agent: "mike", Payload: map[string]any{"detail": detail}})
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
		p.emit(owner, id, session.Event{Type: "file", Agent: "alex", Payload: map[string]any{"path": path}})
	}
	return files, nil
}

func (p *Pipeline) writeFiles(owner, id string, files provider.FileSet) error {
	// The AtomsPay SDK is platform-owned: inject it into every app so a
	// 生成引擎's ecommerce app can import './atomspay' even though the
	// model never emits the file itself. Non-shop apps never import it
	// and esbuild tree-shakes it out of the bundle.
	files[provider.AtomsPaySDKPath] = provider.AtomsPaySDK
	// Defense in depth: files from a directly-injected Prov (tests, future
	// bare providers) bypass the chain's validation — enforce it at the
	// write boundary too.
	if err := provider.Validate(files); err != nil {
		return fmt.Errorf("invalid file set: %w", err)
	}
	appDir := filepath.Join(p.Store.Workspace(owner, id), "app")
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

func (p *Pipeline) setStatus(owner, id, status string) {
	sess, err := p.Store.Load(owner, id)
	if err != nil {
		slog.Error("load session for status update", "session", id, "err", err)
		return
	}
	sess.Status = status
	if err := p.Store.Save(owner, sess); err != nil {
		slog.Error("save session status", "session", id, "status", status, "err", err)
	}
}
