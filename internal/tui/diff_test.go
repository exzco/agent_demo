package tui

import (
	"fmt"
	"strings"
	"testing"
)

func TestSplitLines(t *testing.T) {
	if got := splitLines(""); got != nil {
		t.Fatalf("empty: got %v", got)
	}
	if got := splitLines("a\nb\n"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("trailing newline: got %v", got)
	}
	if got := splitLines("a\nb"); len(got) != 2 {
		t.Fatalf("no trailing newline: got %v", got)
	}
}

func TestDiffStats(t *testing.T) {
	added, removed := DiffStats("a\nb\nc\n", "a\nx\nc\nd\n")
	if added != 2 || removed != 1 {
		t.Fatalf("got +%d/-%d, want +2/-1", added, removed)
	}
	added, removed = DiffStats("", "a\nb")
	if added != 2 || removed != 0 {
		t.Fatalf("pure add: got +%d/-%d", added, removed)
	}
	if added, removed = DiffStats("same", "same"); added != 0 || removed != 0 {
		t.Fatalf("identical: got +%d/-%d", added, removed)
	}
}

func TestGenerateUnifiedDiffBasic(t *testing.T) {
	withoutColor(t)
	out := GenerateUnifiedDiff("f.txt", "a\nb\nc\n", "a\nx\nc\n")
	for _, want := range []string{
		"--- f.txt (original)",
		"+++ f.txt (modified)",
		"(+1/-1)",
		"@@ -1,3 +1,3 @@",
		"   1 │ a",
		"-   2 │ b",
		"+   2 │ x",
		"   3 │ c",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestGenerateUnifiedDiffIdentical(t *testing.T) {
	withoutColor(t)
	out := GenerateUnifiedDiff("same.txt", "a\nb\n", "a\nb\n")
	if strings.Contains(out, "@@") {
		t.Fatalf("identical content should have no hunks:\n%s", out)
	}
	if strings.Contains(out, "(+0/-0)") {
		t.Fatalf("zero stats should be omitted:\n%s", out)
	}
	if !strings.Contains(out, "same.txt") {
		t.Fatalf("header missing:\n%s", out)
	}
}

func TestGenerateUnifiedDiffOmitHeader(t *testing.T) {
	withoutColor(t)
	out := GenerateUnifiedDiffWithOptions("f.txt", "a\n", "b\n", DiffOptions{OmitHeader: true, ContextLines: 3})
	if strings.Contains(out, "(original)") || strings.Contains(out, "(modified)") {
		t.Fatalf("header should be omitted:\n%s", out)
	}
	if !strings.Contains(out, "@@") {
		t.Fatalf("hunk missing:\n%s", out)
	}
}

func TestGenerateUnifiedDiffFolding(t *testing.T) {
	withoutColor(t)
	var oldB, newB strings.Builder
	for i := 1; i <= 60; i++ {
		oldB.WriteString(fmt.Sprintf("line %02d\n", i))
		if i == 3 || i == 50 {
			newB.WriteString(fmt.Sprintf("line %02d CHANGED\n", i))
		} else {
			newB.WriteString(fmt.Sprintf("line %02d\n", i))
		}
	}
	out := GenerateUnifiedDiffWithOptions("f.txt", oldB.String(), newB.String(), DiffOptions{ContextLines: 3, MaxLineWidth: 400})
	if !strings.Contains(out, "40 unchanged line(s)") {
		t.Fatalf("expected fold marker between hunks:\n%s", out)
	}
	if n := strings.Count(out, "@@ -"); n != 2 {
		t.Fatalf("expected 2 hunk headers, got %d:\n%s", n, out)
	}
}

func TestGenerateUnifiedDiffTruncation(t *testing.T) {
	withoutColor(t)
	var newB strings.Builder
	for i := 0; i < 20; i++ {
		newB.WriteString(fmt.Sprintf("new line %d\n", i))
	}
	out := GenerateUnifiedDiffWithOptions("f.txt", "", newB.String(), DiffOptions{MaxOutputLines: 5})
	if !strings.Contains(out, "diff truncated at 5 lines") {
		t.Fatalf("expected truncation notice:\n%s", out)
	}
}

func TestGenerateUnifiedDiffLongLine(t *testing.T) {
	withoutColor(t)
	longA := strings.Repeat("a", 100)
	longB := strings.Repeat("b", 100)
	out := GenerateUnifiedDiffWithOptions("f.txt", longA+"\n", longB+"\n", DiffOptions{MaxLineWidth: 10})
	if !strings.Contains(out, "…") {
		t.Fatalf("expected ellipsis for long line:\n%s", out)
	}
	if strings.Contains(out, longA) {
		t.Fatalf("long line should be truncated:\n%s", out)
	}
}

func TestGenerateUnifiedDiffBetweenNames(t *testing.T) {
	withoutColor(t)
	out := GenerateUnifiedDiffBetween("old.txt", "new.txt", "a\n", "b\n", DiffOptions{})
	if !strings.Contains(out, "--- old.txt (original)") {
		t.Fatalf("missing old name:\n%s", out)
	}
	if !strings.Contains(out, "+++ new.txt (modified)") {
		t.Fatalf("missing new name:\n%s", out)
	}
}

func TestGenerateUnifiedDiffNewFile(t *testing.T) {
	withoutColor(t)
	out := GenerateUnifiedDiff("new.txt", "", "hello\n")
	if !strings.Contains(out, "@@ -1,0 +1,1 @@") {
		t.Fatalf("unexpected hunk header for new file:\n%s", out)
	}
	if !strings.Contains(out, "+   1 │ hello") {
		t.Fatalf("missing added line:\n%s", out)
	}
}
