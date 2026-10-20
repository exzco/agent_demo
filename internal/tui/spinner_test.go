package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestSpinnerNoopOnNonTerminal(t *testing.T) {
	var buf bytes.Buffer
	s := NewSpinner(&buf)
	s.Start("thinking…")

	if s.Final() {
		t.Fatal("Final should return false when output is not a terminal")
	}
	if s.Final() {
		t.Fatal("second Final should also return false")
	}
	if buf.Len() != 0 {
		t.Fatalf("non-terminal spinner should print nothing, got %q", buf.String())
	}

	s.Stop() // must not panic or print
	if buf.Len() != 0 {
		t.Fatalf("Stop should print nothing for non-terminal spinner, got %q", buf.String())
	}
}

func TestBuildFooterRows(t *testing.T) {
	lines := []string{"ACode 1.0 │ Context: 1k"}
	rows := BuildFooterRows(lines, 40)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if !strings.Contains(rows[0], "───") {
		t.Errorf("row 0 should be divider rule: %q", rows[0])
	}
	if !strings.Contains(rows[1], "ACode 1.0") {
		t.Errorf("row 1 should contain status text: %q", rows[1])
	}
}

func TestSpinnerWithFooter(t *testing.T) {
	var buf bytes.Buffer
	s := NewSpinner(&buf)
	s.forceTTY = true
	s.SetFooter(func() []string {
		return []string{"ACode 1.0 │ Perm: workspace"}
	})

	s.Start("thinking…")
	time.Sleep(30 * time.Millisecond)

	out := buf.String()
	if !strings.Contains(out, "thinking…") {
		t.Fatalf("expected thinking… in output: %q", out)
	}
	if !strings.Contains(out, "ACode 1.0") {
		t.Fatalf("expected footer in output: %q", out)
	}
	if !strings.Contains(out, "───") {
		t.Fatalf("expected divider in output: %q", out)
	}

	s.Stop()
	outAfter := buf.String()
	if !strings.Contains(outAfter, "\x1b[2A\r") {
		t.Fatalf("expected cursor repositioning in output: %q", outAfter)
	}
}

func TestSpinnerFinalWithFooter(t *testing.T) {
	var buf bytes.Buffer
	s := NewSpinner(&buf)
	s.forceTTY = true
	s.SetFooter(func() []string {
		return []string{"ACode 1.0 │ Perm: workspace"}
	})

	s.Start("thinking…")
	time.Sleep(20 * time.Millisecond)
	if !s.Final() {
		t.Fatal("Final must return true when forceTTY is enabled")
	}
	out := buf.String()
	if !strings.Contains(out, "thinking…") {
		t.Fatalf("expected frozen line in output: %q", out)
	}
}
