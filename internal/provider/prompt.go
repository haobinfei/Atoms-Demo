package provider

import (
	"fmt"
	"sort"
	"strings"
)

const (
	originalPromptMarker = "【原始需求】"
	currentFilesMarker   = "【当前版本文件】"
	historyPromptMarker  = "【历史迭代要求】"
	executionRuleMarker  = "【执行规则】"
	latestPromptMarker   = "【本轮最新要求｜最高优先级，必须执行】"
)

// BuildIterationPrompt formats an iteration turn for every generation engine.
// The latest request is deliberately placed last and separated from the
// current files: recency and the explicit marker make it the binding
// instruction even when an older request conflicts with it.
func BuildIterationPrompt(original string, files map[string]string, iterations []string) string {
	if len(iterations) == 0 {
		return original
	}

	var b strings.Builder
	b.WriteString(originalPromptMarker)
	b.WriteString("\n")
	b.WriteString(original)
	b.WriteString("\n\n")
	b.WriteString(currentFilesMarker)
	b.WriteString("\n")
	if len(files) == 0 {
		b.WriteString("（无）")
	} else {
		b.WriteString("以下是当前版本，仅作为修改基线；如果与本轮最新要求冲突，以最新要求为准。")
		paths := make([]string, 0, len(files))
		for path := range files {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			fmt.Fprintf(&b, "\n\n--- %s ---\n%s", path, files[path])
		}
	}
	b.WriteString("\n\n")
	b.WriteString(historyPromptMarker)
	b.WriteString("\n")
	if len(iterations) <= 1 {
		b.WriteString("（无）")
	} else {
		for i, request := range iterations[:len(iterations)-1] {
			fmt.Fprintf(&b, "%d. %s\n", i+1, request)
		}
	}
	b.WriteString("\n")
	b.WriteString(executionRuleMarker)
	b.WriteString("\n")
	b.WriteString("- 必须实际落实本轮最新要求，不得原样返回当前版本。\n")
	b.WriteString("- 最新要求与原始需求或历史要求冲突时，以最新要求为准。\n")
	b.WriteString("- 保持未要求修改的部分稳定，并输出完整、可构建的新文件集。")
	b.WriteString("\n\n")
	b.WriteString(latestPromptMarker)
	b.WriteString("\n")
	b.WriteString(strings.TrimSpace(iterations[len(iterations)-1]))
	return b.String()
}

// promptIntentText returns only the user-authored request sections. Current
// file contents are excluded so quoted code (for example className="card")
// cannot be mistaken for a product name or a new theme requirement.
func promptIntentText(prompt string) (base, latest string) {
	withoutLatest := prompt
	if i := strings.LastIndex(prompt, latestPromptMarker); i >= 0 {
		latest = strings.TrimSpace(prompt[i+len(latestPromptMarker):])
		withoutLatest = prompt[:i]
	}
	if !strings.Contains(withoutLatest, originalPromptMarker) {
		return strings.TrimSpace(withoutLatest), latest
	}
	original := promptSection(withoutLatest, originalPromptMarker, currentFilesMarker)
	history := promptSection(withoutLatest, historyPromptMarker, executionRuleMarker)
	return strings.TrimSpace(original + "\n" + history), latest
}

func promptSection(text, start, end string) string {
	i := strings.Index(text, start)
	if i < 0 {
		return ""
	}
	rest := text[i+len(start):]
	if end != "" {
		if j := strings.Index(rest, end); j >= 0 {
			rest = rest[:j]
		}
	}
	return strings.TrimSpace(rest)
}
