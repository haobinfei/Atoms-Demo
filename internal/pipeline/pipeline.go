// Package pipeline orchestrates the AI 团队 through a single 回合 (Turn):
// Mike plans, Emma specifies, Bob designs the architecture, Alex generates
// code, Sarah adds metadata. Every step appends to the session's 事件流.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/haobinfei/Atoms-Demo/internal/artifact"
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
	// Artifacts stores each successful build as an immutable snapshot and
	// activates it for /app. nil keeps the legacy local dist behavior.
	Artifacts *artifact.Store
	// StepDelay keeps the timeline human-readable in demos; tests may shrink it.
	StepDelay time.Duration
}

func (p *Pipeline) emit(owner, id string, ev session.Event) {
	appended, err := p.Store.AppendEvent(owner, id, ev)
	if err != nil {
		slog.Error("append event", "session", id, "type", ev.Type, "err", err)
		return
	}
	p.PublishEvent(id, appended)
}

// PublishEvent fans an event the Store already persisted out to live SSE
// clients. Publishing is best effort: the event is durable, so a failed
// fan-out only delays it until the client's next replay (ADR-0007).
func (p *Pipeline) PublishEvent(id string, ev session.Event) {
	if p.Bus == nil {
		return
	}
	// Postgres already holds the event; Redis fan-out is a hot path, so a
	// publish failure must never fail or roll back the turn (ADR-0007).
	if err := p.Bus.Publish(context.Background(), id, ev); err != nil {
		slog.Warn("publish event", "session", id, "seq", ev.Seq, "err", err)
	}
}

// errCanceled aborts the running 回合 without failing it: the session returns
// to 就绪 and the previous version stays live (ADR-0013).
var errCanceled = errors.New("turn canceled")

// canceled reports whether a 取消回合 (Cancel Turn) request is outstanding for
// the 回合 now executing. The request is the newest event of its kind, and it
// only counts while no terminal event (完成/出错) is newer: a turn that has
// already ended closes the window, so a request that raced the end of a turn
// cannot cancel the next one.
//
// The flag is read back from the Store rather than held in process memory, so
// this works when the request was served by a different replica and when the
// 回合 is re-run after a restart (ADR-0013).
func (p *Pipeline) canceled(owner, id string) bool {
	events, err := p.Store.EventsAfter(owner, id, 0)
	if err != nil {
		// A read failure must not abort a healthy turn: the next check
		// retries, and the request stays on the stream until then.
		slog.Warn("read events for cancel check", "session", id, "err", err)
		return false
	}
	requested, terminal := 0, 0
	for _, ev := range events {
		switch ev.Type {
		case session.CancelEventType:
			requested = ev.Seq
		case "done", "error":
			terminal = ev.Seq
		}
	}
	return requested > terminal
}

// finishCanceled ends a 回合 the user aborted. The session goes back to 就绪
// — never 失败 — and the previous version is untouched, because the abort
// happens before the file writes that commit a turn. The terminal 完成 event
// is what tells the client the turn stopped, and what closes the cancel
// window so the next turn is not aborted by this request.
func (p *Pipeline) finishCanceled(owner, id string) {
	p.setStatus(owner, id, "ready")
	msg := session.Message{Role: "agent", Agent: "mike", Content: "本回合已取消，保留上一版产物。"}
	if err := p.Store.AppendMessage(owner, id, msg); err != nil {
		slog.Error("append cancel summary", "session", id, "err", err)
	}
	p.emit(owner, id, session.Event{Type: "done", Agent: "mike", Payload: map[string]any{
		"status":   "ready",
		"canceled": true,
		"detail":   "本回合已取消，保留上一版产物",
	}})
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
	return provider.BuildIterationPrompt(user[0], files, user[1:])
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

// 回合 stages, in execution order. They are reported on a failure so the
// client can say where the turn broke instead of only what the error said
// (ticket #30).
const (
	StagePlan         = "plan"
	StageSpec         = "spec"
	StageArchitecture = "architecture"
	StageGenerate     = "generate"
	StageWrite        = "write"
	StageBuild        = "build"
)

// stageHints turn a stage into something the user can act on. A bare provider
// error tells them nothing about what to do next.
var stageHints = map[string]string{
	StagePlan:         "模型没能拆解这个需求。把想法说得更具体一些再试一次。",
	StageSpec:         "整理产品规格时失败，通常是模型返回不可用。重试会重新调用模型。",
	StageArchitecture: "设计架构时失败，通常是模型返回不可用。重试会重新调用模型。",
	StageGenerate:     "生成代码时失败，可能是模型不可用或输出无法解析。重试会重新调用模型。",
	StageWrite:        "生成的代码没有通过校验。重试会重新生成；若连续失败，把需求拆小一些。",
	StageBuild:        "生成的代码无法构建。重试会重新生成；若连续失败，把需求拆小一些。",
}

// Run executes the full agent sequence for the session's current prompt.
func (p *Pipeline) Run(ctx context.Context, owner, id string) {
	fail := func(stage string, err error) {
		payload := map[string]any{"message": err.Error(), "stage": stage}
		if hint, ok := stageHints[stage]; ok {
			payload["hint"] = hint
		}
		p.emit(owner, id, session.Event{Type: "error", Payload: payload})
	}
	files, stage, err := p.run(ctx, owner, id)
	if errors.Is(err, errCanceled) {
		p.finishCanceled(owner, id)
		return
	}
	status := "ready"
	if err != nil {
		status = "failed"
		fail(stage, err)
	}
	if files != nil {
		if err := p.writeFiles(owner, id, files); err != nil {
			status = "failed"
			fail(StageWrite, err)
		} else if p.Builder != nil {
			appDir := filepath.Join(p.Store.Workspace(owner, id), "app")
			distDir := filepath.Join(p.Store.Workspace(owner, id), "dist")
			if err := p.Builder.Build(appDir, distDir); err != nil {
				// Drop any stale dist so /app never serves an outdated build
				// while the session is failed.
				_ = os.RemoveAll(distDir)
				p.dropArtifacts(id)
				status = "failed"
				fail(StageBuild, err)
			} else if p.Artifacts != nil {
				if _, err := p.Artifacts.PutTree(ctx, artifact.SessionPrefix(id), distDir); err != nil {
					// A multi-replica /app reads from the shared store, so a
					// failed upload must fail the turn rather than expose a
					// version only one replica can see.
					_ = os.RemoveAll(distDir)
					p.dropArtifacts(id)
					status = "failed"
					fail(StageBuild, fmt.Errorf("store build: %w", err))
				}
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

// run executes the 流水线 stages and reports which stage failed, so Run can
// label the error event. An empty stage means the failure was not inside a
// stage (e.g. the session could not be loaded). A 取消回合 (Cancel Turn)
// request is reported as errCanceled, which Run turns into a 就绪 session
// rather than a failure.
func (p *Pipeline) run(ctx context.Context, owner, id string) (provider.FileSet, string, error) {
	sess, err := p.Store.Load(owner, id)
	if err != nil {
		return nil, "", err
	}
	step := func(agent, typ, detail string) error {
		if p.canceled(owner, id) {
			return errCanceled
		}
		if p.StepDelay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(p.StepDelay):
			}
		}
		payload := map[string]any{"detail": detail}
		p.emit(owner, id, session.Event{Type: typ, Agent: agent, Payload: payload})
		return nil
	}

	if err := step("mike", "plan", "拆解需求并制定执行计划"); err != nil {
		return nil, StagePlan, err
	}
	if err := step("emma", "spec", fmt.Sprintf("为「%s」整理产品规格", sess.Prompt)); err != nil {
		return nil, StageSpec, err
	}
	if err := step("bob", "architecture", "设计系统结构：React + TypeScript，组件化组织"); err != nil {
		return nil, StageArchitecture, err
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
		return nil, StageGenerate, err
	}
	// Checked after 生成 and before the file events: a turn commits at
	// writeFiles, so aborting here is what keeps the previous version whole.
	// The 生成 call itself is the longest step, so this is the point where a
	// cancel has the most to save.
	if p.canceled(owner, id) {
		return nil, "", errCanceled
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
	return files, "", nil
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

// dropArtifacts removes the active build after a failed turn, matching the
// legacy behavior of deleting a stale local dist. Cleanup is best effort and
// uses a fresh context because the Job context may already be canceled.
func (p *Pipeline) dropArtifacts(id string) {
	if p.Artifacts == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.Artifacts.ClearCurrent(ctx, artifact.SessionPrefix(id)); err != nil {
		slog.Warn("drop failed build artifacts", "session", id, "err", err)
	}
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
