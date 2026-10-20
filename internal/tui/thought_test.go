package tui

import (
	"strings"
	"testing"
	"time"
)

func TestThoughtHeaderFormat(t *testing.T) {
	withoutColor(t)

	got := ThoughtHeader(17*time.Second, 2134, false)
	if got != "▸ Thought for 17s, 2.1k tokens" {
		t.Fatalf("collapsed header: %q", got)
	}
	got = ThoughtHeader(87*time.Second, 850, true)
	if got != "▾ Thought for 1m 27s, 850 tokens" {
		t.Fatalf("expanded header: %q", got)
	}
	got = ThoughtHeader(900*time.Millisecond, 0, false)
	if got != "▸ Thought for 0.9s, 0 tokens" {
		t.Fatalf("sub-second header: %q", got)
	}
}

func TestEstimateTokensAndFormat(t *testing.T) {
	if got := EstimateTokens(strings.Repeat("a", 40)); got != 10 {
		t.Fatalf("ascii estimate: %d", got)
	}
	if got := EstimateTokens("中文四字"); got != 4 {
		t.Fatalf("cjk estimate: %d", got)
	}
	if got := FormatTokenCount(999); got != "999" {
		t.Fatalf("count below 1k: %q", got)
	}
	if got := FormatTokenCount(2134); got != "2.1k" {
		t.Fatalf("count above 1k: %q", got)
	}
}

func TestRenderThoughtCollapsed(t *testing.T) {
	withoutColor(t)

	rows := RenderThoughtCollapsed("first line\n\nsecond line\nthird line", 5*time.Second, 42, 60)
	if len(rows) != 3 {
		t.Fatalf("expected header + 2 preview rows, got %d: %q", len(rows), rows)
	}
	if rows[0] != "▸ Thought for 5s, 42 tokens" {
		t.Fatalf("header: %q", rows[0])
	}
	if rows[1] != "  first line" || rows[2] != "  second line" {
		t.Fatalf("preview rows: %q", rows[1:])
	}

	long := strings.Repeat("x", 200)
	rows = RenderThoughtCollapsed(long, time.Second, 1, 40)
	if w := displayWidthANSI(rows[1]); w > 40 {
		t.Fatalf("preview must fit width: %d cols", w)
	}
	if !strings.Contains(rows[1], "…") {
		t.Fatalf("truncated preview should carry an ellipsis: %q", rows[1])
	}
}

func TestRenderThoughtExpandedWraps(t *testing.T) {
	withoutColor(t)

	rows := RenderThoughtExpanded("alpha beta gamma delta epsilon", 3*time.Second, 12, 16)
	if rows[0] != "▾ Thought for 3s, 12 tokens" {
		t.Fatalf("header: %q", rows[0])
	}
	for _, row := range rows[1:] {
		if w := displayWidthANSI(row); w > 16 {
			t.Fatalf("row too wide: %d cols in %q", w, row)
		}
	}
	if strings.Join(rows[1:], "") == "" {
		t.Fatalf("expanded body missing: %q", rows)
	}

	// CJK text must not exceed the column budget either.
	rows = RenderThoughtExpanded(strings.Repeat("宽", 30), time.Second, 30, 14)
	for _, row := range rows[1:] {
		if w := displayWidthANSI(row); w > 14 {
			t.Fatalf("cjk row too wide: %d cols in %q", w, row)
		}
	}
}
