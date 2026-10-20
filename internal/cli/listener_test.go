package cli

import (
	"bytes"
	"strings"
	"testing"

	"agy/internal/tui"
	"agy/internal/types"
)

// withoutColor disables ANSI coloring for the duration of a test.
func withoutColor(t *testing.T) {
	t.Helper()
	saved := tui.ColorEnabled()
	tui.SetColorEnabled(false)
	t.Cleanup(func() { tui.SetColorEnabled(saved) })
}

func TestOnToolResultStoresOutput(t *testing.T) {
	withoutColor(t)
	var buf bytes.Buffer
	l := &Listener{Out: &buf, Spinner: tui.NewSpinner(&buf)}
	l.BeginTurn()
	call := types.ToolCall{Function: types.FunctionCall{Name: "run_command", Arguments: `{"CommandLine":"ls"}`}}

	l.OnToolCall(call)
	l.OnToolResult(call, "line1\nline2", nil)

	if l.toolEntry == nil || !l.toolEntry.hasResult {
		t.Fatal("tool result must be recorded on the turn entry")
	}
	if l.toolEntry.result != "line1\nline2" {
		t.Fatalf("stored result: %q", l.toolEntry.result)
	}
	// Non-TTY output keeps the full payload visible (no Ctrl+O available).
	out := buf.String()
	if !strings.Contains(out, "line1") || !strings.Contains(out, "line2") {
		t.Fatalf("non-tty run must print the full output: %q", out)
	}
}

func TestToggleDetailsRequiresTTY(t *testing.T) {
	var buf bytes.Buffer
	l := &Listener{Out: &buf}
	l.turnRows = []*turnEntry{{kind: turnThought, thought: &thoughtBlock{text: "x"}}}
	if l.ToggleDetails(0) {
		t.Fatal("Ctrl+O repaint must be a no-op outside an interactive terminal")
	}
}

func TestRenderTurnRowsTogglesToolOutput(t *testing.T) {
	withoutColor(t)
	l := &Listener{Out: &bytes.Buffer{}}
	l.turnRows = []*turnEntry{
		{kind: turnToolRow, text: "● Bash(ls)", hasResult: true, result: "alpha\nbeta"},
	}

	rows := l.renderTurnRowsLocked()
	want := []string{"● Bash(ls)", "  … 2 lines hidden · Ctrl+O to expand"}
	if len(rows) != len(want) || rows[0] != want[0] || rows[1] != want[1] {
		t.Fatalf("collapsed rows: %q", rows)
	}
	if got := l.turnRowCountLocked(); got != 2 {
		t.Fatalf("collapsed row count: %d", got)
	}

	l.detailsExpanded = true
	rows = l.renderTurnRowsLocked()
	want = []string{"● Bash(ls)", "  alpha", "  beta"}
	if len(rows) != len(want) || rows[1] != want[1] || rows[2] != want[2] {
		t.Fatalf("expanded rows: %q", rows)
	}
	if got := l.turnRowCountLocked(); got != 3 {
		t.Fatalf("expanded row count: %d", got)
	}
}

func TestBeginTurnConfiguresSpinnerFooter(t *testing.T) {
	var buf bytes.Buffer
	app := tui.NewApp("deepseek-chat", "https://api.deepseek.com")
	spinner := tui.NewSpinner(&buf)
	l := &Listener{Out: &buf, Spinner: spinner, App: app}

	l.BeginTurn()

	if spinner.Footer() == nil {
		t.Fatal("expected spinner.Footer() to be configured by BeginTurn")
	}
	lines := spinner.Footer()()
	if len(lines) == 0 || !strings.Contains(lines[0], "deepseek-chat") {
		t.Fatalf("expected status bar with deepseek-chat, got %v", lines)
	}
}
