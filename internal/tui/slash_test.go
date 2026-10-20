package tui

import (
	"strings"
	"testing"
)

func TestDefaultSlashCommands(t *testing.T) {
	cmds := DefaultSlashCommands()
	if len(cmds) != 53 {
		t.Fatalf("expected 53 official slash commands, got %d", len(cmds))
	}

	// Verify top 5 match user screenshot exactly
	expectedTop5 := []string{
		"/add-dir",
		"/agents",
		"/artifact",
		"/btw",
		"/changelog",
	}
	for i, exp := range expectedTop5 {
		if cmds[i].Name != exp {
			t.Fatalf("expected index %d to be %s, got %s", i, exp, cmds[i].Name)
		}
	}
}

func TestRenderSlashDropdown(t *testing.T) {
	cmds := DefaultSlashCommands()
	rendered, lineCount := RenderSlashDropdown(cmds, 0, 5)

	// Check divider
	if !strings.Contains(rendered, "────────────────────────────────────────────────────────────────────────") {
		t.Fatalf("expected horizontal divider line")
	}

	// Check top selected item has '> /add-dir'
	if !strings.Contains(rendered, "/add-dir") {
		t.Fatalf("expected /add-dir in rendered output")
	}

	// Check '↓ 48 more'
	if !strings.Contains(rendered, "↓ 48 more") {
		t.Fatalf("expected '↓ 47 more' in dropdown footer, got:\n%s", rendered)
	}

	if lineCount != 7 { // 1 divider + 5 items + 1 more indicator
		t.Fatalf("expected 7 lines rendered, got %d", lineCount)
	}
}

func TestGetSlashDropdownLinesWidthFits(t *testing.T) {
	cmds := DefaultSlashCommands()
	for _, width := range []int{20, 40, 60, 71, 72, 83} {
		lines := GetSlashDropdownLinesWidth(cmds, 0, 5, width)
		if len(lines) != 7 {
			t.Fatalf("width %d: expected 7 lines, got %d", width, len(lines))
		}
		for i, line := range lines {
			if w := displayWidthANSI(line); w > width {
				t.Errorf("width %d line %d too wide (%d cols): %q", width, i, w, stripANSI(line))
			}
		}
	}
}

func TestGetSlashDropdownLinesWidthLegacy(t *testing.T) {
	cmds := DefaultSlashCommands()
	legacy := strings.Join(GetSlashDropdownLines(cmds, 0, 5), "|")
	wide := strings.Join(GetSlashDropdownLinesWidth(cmds, 0, 5, 100), "|")
	if legacy != wide {
		t.Fatalf("wide terminals must keep the legacy layout")
	}
}
