// Competition orchestration: 参赛者 (contestants) generate in parallel
// goroutines, each builds independently; when all are finished the
// 评审 Nina scores every ready version on three dimensions. The user
// picks the winner; losers stay archived under contest/<provider>/.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/session"
)

// ScoreFunc is Nina's verdict: three dimension scores plus a textual
// comment. Swappable in tests; the default scores via LLM when one is
// reachable and falls back to a deterministic rubric so a contest never
// wedges on an LLM outage.
type ScoreFunc func(ctx context.Context, prompt string, files provider.FileSet) (session.Scores, string, error)

// DefaultScoreFunc tries the given LLM as Nina's brain and falls back to
// the heuristic on any failure (garbled reply, outage, nil llm).
func DefaultScoreFunc(llm provider.Provider) ScoreFunc {
	return func(ctx context.Context, prompt string, files provider.FileSet) (session.Scores, string, error) {
		if llm != nil {
			if scores, comment, err := scoreWithLLM(ctx, llm, prompt, files); err == nil {
				return scores, comment, nil
			}
			slog.Warn("nina llm scoring failed, using heuristic", "llm", llm.Name())
		}
		return heuristicScore(files), "Nina（启发式评分：LLM 不可用）", nil
	}
}

// scoreWithLLM asks for a strict JSON verdict and parses the first JSON
// object in the reply; anything unparsable is an error so the caller can
// fall back.
func scoreWithLLM(ctx context.Context, llm provider.Provider, prompt string, files provider.FileSet) (session.Scores, string, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "你是应用评审 Nina。请为以下需求生成的应用打分（0-10 整数），三维度：design 设计、functionality 功能完整度、codeQuality 代码质量，并给一句中文点评 comment。只输出 JSON：{\"design\":n,\"functionality\":n,\"codeQuality\":n,\"comment\":\"...\"}\n\n需求：%s\n\n文件：", prompt)
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	// Bound the prompt so a huge file set cannot blow the LLM's context
	// window; truncated contents are enough for a score.
	const perFileCap, totalCap = 2048, 16384
	total := 0
	for _, path := range paths {
		content := files[path]
		if len(content) > perFileCap {
			content = content[:perFileCap] + "\n…(截断)"
		}
		if total+len(content) > totalCap {
			content = content[:max(0, totalCap-total)]
		}
		total += len(content)
		fmt.Fprintf(&sb, "\n--- %s ---\n%s", path, content)
		if total >= totalCap {
			break
		}
	}
	out, err := llm.Generate(ctx, sb.String(), func(string, string) {})
	if err != nil {
		return session.Scores{}, "", err
	}
	var verdict struct {
		Design        int    `json:"design"`
		Functionality int    `json:"functionality"`
		CodeQuality   int    `json:"codeQuality"`
		Comment       string `json:"comment"`
	}
	reply := ""
	for _, content := range out {
		reply += content
	}
	// Tolerate prose around the JSON object.
	start, end := strings.Index(reply, "{"), strings.LastIndex(reply, "}")
	if start < 0 || end <= start {
		return session.Scores{}, "", fmt.Errorf("no json in reply")
	}
	if err := json.Unmarshal([]byte(reply[start:end+1]), &verdict); err != nil {
		return session.Scores{}, "", err
	}
	return session.Scores{
		Design:        clampScore(verdict.Design),
		Functionality: clampScore(verdict.Functionality),
		CodeQuality:   clampScore(verdict.CodeQuality),
	}, verdict.Comment, nil
}

// heuristicScore is the deterministic fallback: richer file sets score
// higher on every dimension.
func heuristicScore(files provider.FileSet) session.Scores {
	size, lines := 0, 0
	for _, content := range files {
		size += len(content)
		lines += strings.Count(content, "\n")
	}
	return session.Scores{
		Design:        clampScore(3 + len(files)),
		Functionality: clampScore(3 + size/400),
		CodeQuality:   clampScore(3 + lines/40),
	}
}

func clampScore(n int) int {
	if n < 0 {
		return 0
	}
	if n > 10 {
		return 10
	}
	return n
}

// runContest executes one competition asynchronously: one goroutine per
// contestant, then Nina scores all ready versions, then the session turns
// ready and the user picks a winner.
func (s *Server) runContest(user, id, prompt string) {
	competitors := s.competitors()
	// Assign each contestant a unique entry name: two same-named
	// providers (possible via Config.Competitors) would otherwise race
	// on the same contest/<name>/{app,dist} directories on disk. The
	// second "gemma" becomes "gemma-2".
	counts := make(map[string]int, len(competitors))
	assigned := make([]string, len(competitors))
	names := make([]string, 0, len(competitors))
	for i, c := range competitors {
		counts[c.Name()]++
		name := c.Name()
		if counts[name] > 1 {
			name = fmt.Sprintf("%s-%d", c.Name(), counts[name])
		}
		assigned[i] = name
		names = append(names, name)
	}
	if err := s.store.StartContest(user, id, names); err != nil {
		slog.Error("start contest", "session", id, "err", err)
		// Best-effort bail-out so the session cannot strand in
		// "building" with no contest and no events.
		_ = s.store.SetStatus(user, id, "failed")
		s.pipeline.EmitError(user, id, "竞赛初始化失败，请重试")
		return
	}
	s.pipeline.EmitContestStatus(user, id, "start",
		fmt.Sprintf("竞赛开始：%s 并行生成中", strings.Join(names, "、")))

	var wg sync.WaitGroup
	for i, c := range competitors {
		wg.Add(1)
		name := assigned[i]
		go func(comp provider.Provider, name string) {
			defer wg.Done()
			s.runContestant(user, id, prompt, comp, name)
		}(c, name)
	}
	wg.Wait()

	s.scoreContest(user, id, prompt)
}

// runContestant drives one 参赛者: generate → validate → write → build,
// all under contest/<provider>/ so versions stay independent.
func (s *Server) runContestant(user, id, prompt string, comp provider.Provider, name string) {
	ws := s.store.Workspace(user, id)
	appDir := filepath.Join(ws, "contest", name, "app")
	distDir := filepath.Join(ws, "contest", name, "dist")
	fail := func(err error) {
		slog.Error("contestant failed", "session", id, "provider", name, "err", err)
		_ = s.store.UpdateContestEntry(user, id, name, func(entry *session.ContestEntry) {
			entry.Status = "failed"
		})
		s.pipeline.EmitContestStatus(user, id, "failed", fmt.Sprintf("%s 生成失败：%s", name, err.Error()))
	}

	files, err := comp.Generate(context.Background(), prompt, func(string, string) {})
	if err != nil {
		fail(err)
		return
	}
	if err := provider.Validate(files); err != nil {
		fail(fmt.Errorf("invalid files: %w", err))
		return
	}
	if err := writeFileSet(appDir, files); err != nil {
		fail(err)
		return
	}
	if err := s.builder.Build(appDir, distDir); err != nil {
		fail(err)
		return
	}
	_ = s.store.UpdateContestEntry(user, id, name, func(entry *session.ContestEntry) {
		entry.Status = "ready"
	})
	s.pipeline.EmitContestStatus(user, id, "ready", fmt.Sprintf("%s 构建完成", name))
}

// scoreContest is Nina's judging pass over every ready version; graceful
// degradation means a contest with zero ready versions still finishes.
func (s *Server) scoreContest(user, id, prompt string) {
	sess, err := s.store.Load(user, id)
	if err != nil {
		slog.Error("load session for scoring", "session", id, "err", err)
		return
	}
	if sess.Contest == nil {
		return
	}
	for i := range sess.Contest.Entries {
		entry := &sess.Contest.Entries[i]
		if entry.Status != "ready" {
			continue
		}
		files, err := readFileSet(filepath.Join(s.store.Workspace(user, id), "contest", entry.Provider, "app"))
		if err != nil {
			slog.Error("read contestant files for scoring", "session", id, "provider", entry.Provider, "err", err)
			continue
		}
		scores, comment, err := s.scoreFunc()(context.Background(), prompt, files)
		if err != nil {
			slog.Error("nina scoring", "session", id, "provider", entry.Provider, "err", err)
			continue
		}
		if err := s.store.UpdateContestEntry(user, id, entry.Provider, func(e *session.ContestEntry) {
			e.Scores = &scores
			e.Comment = comment
		}); err != nil {
			slog.Error("save nina scores", "session", id, "provider", entry.Provider, "err", err)
		}
		s.pipeline.EmitScore(user, id, entry.Provider, scores, comment)
	}
	if err := s.store.FinishContest(user, id); err != nil {
		slog.Error("finish contest", "session", id, "err", err)
	}
	_ = s.store.SetStatus(user, id, "ready")
	s.pipeline.EmitContestDone(user, id)
}

// writeFileSet materializes a provider file set under dir.
func writeFileSet(dir string, files provider.FileSet) error {
	for path, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// readFileSet loads every file of a generated app back into memory.
func readFileSet(dir string) (provider.FileSet, error) {
	files := provider.FileSet{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
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

// handleWinner installs the chosen contest version as the session's
// official app; the losers stay archived under contest/<provider>/.
func (s *Server) handleWinner(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user := s.requestUser(r)
	var req struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Provider) == "" {
		writeError(w, http.StatusBadRequest, "provider is required")
		return
	}
	if _, err := s.store.Load(user, id); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	// Claim the winner atomically under the store lock first; a second
	// concurrent POST then sees ErrWinnerPicked instead of racing the
	// file copies below.
	if err := s.store.ClaimContestWinner(user, id, req.Provider); err != nil {
		switch {
		case errors.Is(err, session.ErrNoContest):
			writeError(w, http.StatusNotFound, "session has no contest")
		case errors.Is(err, session.ErrContestRunning), errors.Is(err, session.ErrWinnerPicked):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	ws := s.store.Workspace(user, id)
	if err := session.CopyDirTree(filepath.Join(ws, "contest", req.Provider, "app"), filepath.Join(ws, "app")); err != nil {
		_ = s.store.ClearContestWinner(user, id)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := session.CopyDirTree(filepath.Join(ws, "contest", req.Provider, "dist"), filepath.Join(ws, "dist")); err != nil {
		_ = s.store.ClearContestWinner(user, id)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.pipeline.EmitContestStatus(user, id, "winner", fmt.Sprintf("胜者：%s，后续迭代将基于该版本", req.Provider))
	writeJSON(w, http.StatusOK, map[string]string{"winner": req.Provider})
}
