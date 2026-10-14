package changes

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Diff limits. A 改动记录 must never block the page that renders it
// (ticket #54), so a pair that is not text, or is too large to diff in
// memory, degrades to a bare "已改动" record instead of a diff body.
const (
	// maxDiffBytes bounds one side of a diffed pair. Every file a build
	// produces is far below this; the limit exists for the pathological
	// case only.
	maxDiffBytes = 128 << 10
	// maxDiffCells bounds the LCS table (lines×lines). A pair beyond it
	// falls back to a whole-file replace diff.
	maxDiffCells = 1 << 22
	// contextLines is the unified-diff context, matching diff(1)'s default.
	contextLines = 3
)

// Textual reports whether content can be line-diffed. A NUL byte or invalid
// UTF-8 means binary: there is no honest line diff for one.
func Textual(s string) bool {
	if strings.ContainsRune(s, 0) {
		return false
	}
	return utf8.ValidString(s)
}

// Unified renders old→new as a unified diff. path labels the file in the
// header; an empty side renders as /dev/null, which is how diff(1) marks an
// added or removed file. An unchanged pair yields "".
func Unified(path, old, new string) string {
	a, noNLA := splitLines(old)
	b, noNLB := splitLines(new)
	ops := script(a, b)
	if !changed(ops) {
		if noNLA == noNLB {
			return ""
		}
		// The only difference is the trailing newline. The line scripts
		// are equal, so render the final line as a change — which is the
		// only way to show the marker diff(1) would print.
		return newlineOnlyDiff(path, ops, noNLA, noNLB)
	}
	var out strings.Builder
	if old == "" {
		out.WriteString("--- /dev/null\n")
	} else {
		fmt.Fprintf(&out, "--- a/%s\n", path)
	}
	if new == "" {
		out.WriteString("+++ /dev/null\n")
	} else {
		fmt.Fprintf(&out, "+++ b/%s\n", path)
	}
	out.WriteString(hunks(ops, noNLA, noNLB))
	return out.String()
}

// splitLines cuts text into lines the way diff does: a trailing newline does
// not produce an empty final line, and a missing one is reported so the diff
// can say so.
func splitLines(s string) (lines []string, noFinalNewline bool) {
	if s == "" {
		return nil, false
	}
	if strings.HasSuffix(s, "\n") {
		return strings.Split(strings.TrimSuffix(s, "\n"), "\n"), false
	}
	return strings.Split(s, "\n"), true
}

// op is one line of the edit script.
type op struct {
	kind byte // ' ' equal, '-' delete, '+' insert
	line string
}

// script computes the shortest edit script between a and b. Common prefix
// and suffix are trimmed first — a 回合 that regenerates an app rewrites
// whole files that mostly match, and trimming keeps both the table small and
// the hunks tight.
func script(a, b []string) []op {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	ops := make([]op, 0, len(a)+len(b))
	for _, line := range a[:p] {
		ops = append(ops, op{' ', line})
	}
	ops = append(ops, middle(a[p:len(a)-s], b[p:len(b)-s])...)
	for _, line := range a[len(a)-s:] {
		ops = append(ops, op{' ', line})
	}
	return ops
}

// middle diffs the two middles left after trimming. A pair too large to
// table degrades to "replace everything", which is still a correct — if
// blunt — diff.
func middle(a, b []string) []op {
	n, m := len(a), len(b)
	if (n+1)*(m+1) > maxDiffCells {
		out := make([]op, 0, n+m)
		for _, line := range a {
			out = append(out, op{'-', line})
		}
		for _, line := range b {
			out = append(out, op{'+', line})
		}
		return out
	}
	// lcs[i][j] is the length of the longest common subsequence of
	// a[i:] and b[j:].
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	out := make([]op, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, op{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, op{'-', a[i]})
			i++
		default:
			out = append(out, op{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, op{'-', a[i]})
	}
	for ; j < m; j++ {
		out = append(out, op{'+', b[j]})
	}
	return out
}

func changed(ops []op) bool {
	for _, o := range ops {
		if o.kind != ' ' {
			return true
		}
	}
	return false
}

// newlineOnlyDiff renders the case where two files have identical lines and
// differ only in whether the last one ends with a newline.
func newlineOnlyDiff(path string, ops []op, noNLA, noNLB bool) string {
	last := ops[len(ops)-1].line
	var out strings.Builder
	if len(ops) == 1 {
		fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n", path, path)
	} else {
		fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n@@ -1,%d +1,%d @@\n", path, path, len(ops), len(ops))
		for _, o := range ops[:len(ops)-1] {
			out.WriteByte(' ')
			out.WriteString(o.line)
			out.WriteByte('\n')
		}
	}
	out.WriteString("-" + last + "\n")
	if noNLA {
		out.WriteString("\\ No newline at end of file\n")
	}
	out.WriteString("+" + last + "\n")
	if noNLB {
		out.WriteString("\\ No newline at end of file\n")
	}
	return out.String()
}

// lastA / lastB are the op indices that consume the final line of each side,
// so a missing trailing newline can be marked on exactly that line.
func lastA(ops []op) int {
	for i := len(ops) - 1; i >= 0; i-- {
		if ops[i].kind != '+' {
			return i
		}
	}
	return -1
}

func lastB(ops []op) int {
	for i := len(ops) - 1; i >= 0; i-- {
		if ops[i].kind != '-' {
			return i
		}
	}
	return -1
}

// hunks walks the edit script and emits unified-diff hunks with
// contextLines of context, grouping changes that sit close enough to share
// one hunk.
func hunks(ops []op, noNLA, noNLB bool) string {
	type span struct{ start, end int }
	var spans []span
	for i, o := range ops {
		if o.kind == ' ' {
			continue
		}
		if len(spans) > 0 && i-spans[len(spans)-1].end-1 <= 2*contextLines {
			spans[len(spans)-1].end = i
			continue
		}
		spans = append(spans, span{i, i})
	}
	la, lb := lastA(ops), lastB(ops)
	var out strings.Builder
	for _, sp := range spans {
		lo := max(0, sp.start-contextLines)
		hi := min(len(ops)-1, sp.end+contextLines)
		aStart, bStart := 0, 0
		for i := 0; i < lo; i++ {
			switch ops[i].kind {
			case ' ':
				aStart++
				bStart++
			case '-':
				aStart++
			case '+':
				bStart++
			}
		}
		aCount, bCount := 0, 0
		for i := lo; i <= hi; i++ {
			switch ops[i].kind {
			case ' ':
				aCount++
				bCount++
			case '-':
				aCount++
			case '+':
				bCount++
			}
		}
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", rangeStr(aStart, aCount), rangeStr(bStart, bCount))
		for i := lo; i <= hi; i++ {
			o := ops[i]
			out.WriteByte(o.kind)
			out.WriteString(o.line)
			out.WriteByte('\n')
			markA := i == la && noNLA && o.kind != '+'
			markB := i == lb && noNLB && o.kind != '-'
			if markA || markB {
				out.WriteString("\\ No newline at end of file\n")
			}
		}
	}
	return out.String()
}

// rangeStr renders one side of a hunk header the way diff(1) does: a single
// line is just its number, and an empty range is anchored to the line before
// it.
func rangeStr(before, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", before)
	}
	if count == 1 {
		return fmt.Sprintf("%d", before+1)
	}
	return fmt.Sprintf("%d,%d", before+1, count)
}

// countChanges tallies the added and removed lines of a rendered diff,
// ignoring the file headers.
func countChanges(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}
