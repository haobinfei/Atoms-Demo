package changes_test

import (
	"strings"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/changes"
)

func TestUnifiedAddedFile(t *testing.T) {
	diff := changes.Unified("src/App.tsx", "", "line one\nline two\n")
	if !strings.HasPrefix(diff, "--- /dev/null\n+++ b/src/App.tsx\n") {
		t.Fatalf("added-file header wrong:\n%s", diff)
	}
	if !strings.Contains(diff, "@@ -0,0 +1,2 @@\n+line one\n+line two\n") {
		t.Fatalf("added-file hunk wrong:\n%s", diff)
	}
}

func TestUnifiedDeletedFile(t *testing.T) {
	diff := changes.Unified("src/App.tsx", "line one\nline two\n", "")
	if !strings.HasPrefix(diff, "--- a/src/App.tsx\n+++ /dev/null\n") {
		t.Fatalf("deleted-file header wrong:\n%s", diff)
	}
	if !strings.Contains(diff, "@@ -1,2 +0,0 @@\n-line one\n-line two\n") {
		t.Fatalf("deleted-file hunk wrong:\n%s", diff)
	}
}

func TestUnifiedMiddleChangeIsOneTightHunk(t *testing.T) {
	// 200 lines, one line changed in the middle. Trimming the common
	// prefix/suffix must leave a single small hunk, not a whole-file one.
	var a, b strings.Builder
	for i := 1; i <= 200; i++ {
		if i == 100 {
			a.WriteString("old value\n")
			b.WriteString("new value\n")
			continue
		}
		line := "line " + strings.Repeat("x", 3) + "\n"
		a.WriteString(line)
		b.WriteString(line)
	}
	diff := changes.Unified("src/App.tsx", a.String(), b.String())
	if got := hunkCount(diff); got != 1 {
		t.Fatalf("want one hunk, got %d:\n%s", got, diff)
	}
	if !strings.Contains(diff, "@@ -97,7 +97,7 @@\n") {
		t.Fatalf("hunk header wrong:\n%s", diff)
	}
	if !strings.Contains(diff, "-old value\n+new value\n") {
		t.Fatalf("change lines missing:\n%s", diff)
	}
	// Context is exactly three lines on each side.
	if !strings.Contains(diff, " line xxx\n-old value\n+new value\n line xxx\n line xxx\n line xxx\n") {
		t.Fatalf("context wrong:\n%s", diff)
	}
}

// hunkCount counts hunk headers; each header carries two "@@" delimiters.
func hunkCount(diff string) int {
	n := 0
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@") {
			n++
		}
	}
	return n
}

func TestUnifiedSeparateChangesAreSeparateHunks(t *testing.T) {
	old := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\nm\nn\no\np\nq\nr\ns\nt\n"
	revised := "a\nB\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\nm\nn\no\np\nq\nr\nS\nt\n"
	diff := changes.Unified("f.txt", old, revised)
	if got := hunkCount(diff); got != 2 {
		t.Fatalf("want two hunks, got %d:\n%s", got, diff)
	}
	if !strings.Contains(diff, "-b\n+B\n") || !strings.Contains(diff, "-s\n+S\n") {
		t.Fatalf("both changes missing:\n%s", diff)
	}
}

func TestUnifiedUnchangedIsEmpty(t *testing.T) {
	if diff := changes.Unified("f.txt", "same\n", "same\n"); diff != "" {
		t.Fatalf("unchanged pair should render no diff, got:\n%s", diff)
	}
}

func TestUnifiedMarksMissingTrailingNewline(t *testing.T) {
	diff := changes.Unified("f.txt", "one\ntwo", "one\ntwo\n")
	if !strings.Contains(diff, "-two\n\\ No newline at end of file\n+two\n") {
		t.Fatalf("trailing-newline marker missing:\n%s", diff)
	}
}

func TestUnifiedInsertionHeader(t *testing.T) {
	diff := changes.Unified("f.txt", "a\nb\n", "a\ninserted\nb\n")
	if !strings.Contains(diff, "@@ -1,2 +1,3 @@\n a\n+inserted\n b\n") {
		t.Fatalf("insertion hunk wrong:\n%s", diff)
	}
}

func TestTextual(t *testing.T) {
	if !changes.Textual("plain text\n") {
		t.Error("plain text must be textual")
	}
	if changes.Textual("png\x89binary") {
		t.Error("a NUL byte means binary")
	}
	if changes.Textual("bad utf8 \xff\xfe") {
		t.Error("invalid UTF-8 means binary")
	}
}

func TestNewRecordKinds(t *testing.T) {
	cases := []struct {
		name, old, new, want string
	}{
		{"added", "", "x\n", changes.KindAdded},
		{"deleted", "x\n", "", changes.KindDeleted},
		{"modified", "x\n", "y\n", changes.KindModified},
		{"unchanged", "x\n", "x\n", changes.KindUnchanged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := changes.NewRecord(7, "src/App.tsx", changes.AuthorAgent, tc.old, tc.new)
			if rec.Kind != tc.want {
				t.Fatalf("kind = %q, want %q", rec.Kind, tc.want)
			}
			if rec.Seq != 7 || rec.Path != "src/App.tsx" || rec.Author != changes.AuthorAgent {
				t.Fatalf("identity fields wrong: %+v", rec)
			}
			if rec.At.IsZero() {
				t.Fatal("At must be set")
			}
		})
	}
}

func TestNewRecordCountsLines(t *testing.T) {
	rec := changes.NewRecord(1, "f.txt", changes.AuthorAgent, "a\nb\nc\n", "a\nB\nc\nd\n")
	if rec.Added != 2 || rec.Removed != 1 {
		t.Fatalf("added = %d, removed = %d, want 2/1\ndiff:\n%s", rec.Added, rec.Removed, rec.Diff)
	}
}

// A binary pair degrades to a bare "changed" record: the page lists the file
// and says it changed, without a diff body to choke on (ticket #54).
func TestNewRecordBinaryDegrades(t *testing.T) {
	rec := changes.NewRecord(1, "img.png", changes.AuthorUser, "\x89PNG\r\n\x1a\n old", "\x89PNG\r\n\x1a\n new")
	if !rec.Binary {
		t.Fatal("binary flag must be set")
	}
	if rec.Diff != "" {
		t.Fatalf("a binary record must carry no diff, got:\n%s", rec.Diff)
	}
	if rec.Kind != changes.KindModified {
		t.Fatalf("kind = %q, want modified", rec.Kind)
	}
}

// An oversized pair degrades the same way, so one huge file can never block
// the page that lists a turn's changes.
func TestNewRecordTooLargeDegrades(t *testing.T) {
	big := strings.Repeat("x", 200<<10)
	rec := changes.NewRecord(1, "big.txt", changes.AuthorAgent, "small\n", big)
	if !rec.TooLarge {
		t.Fatal("tooLarge flag must be set")
	}
	if rec.Diff != "" {
		t.Fatal("an oversized record must carry no diff")
	}
}

// A pair too big to table falls back to a whole-file replace, which is still
// a correct diff.
func TestUnifiedHugePairFallsBackToReplace(t *testing.T) {
	var a, b strings.Builder
	for i := 0; i < 3000; i++ {
		a.WriteString("a line\n")
		b.WriteString("b line\n")
	}
	diff := changes.Unified("big.txt", a.String(), b.String())
	if !strings.HasPrefix(diff, "--- a/big.txt\n+++ b/big.txt\n") {
		t.Fatalf("header wrong:\n%s", diff)
	}
	if got := strings.Count(diff, "-a line\n"); got != 3000 {
		t.Fatalf("want 3000 removals, got %d", got)
	}
	if got := strings.Count(diff, "+b line\n"); got != 3000 {
		t.Fatalf("want 3000 additions, got %d", got)
	}
}
