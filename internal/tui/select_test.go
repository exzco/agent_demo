package tui

import (
	"strings"
	"testing"
)

func TestFilterSelectItems(t *testing.T) {
	items := []SelectItem{
		{Key: "url", Label: "LLM Gateway URL", Tag: "https://x"},
		{Key: "key", Label: "API Key", Tag: "sk-***"},
		{Key: "model", Label: "Model", Tag: "gpt-4o"},
	}
	if got := filterSelectItems(items, ""); len(got) != 3 {
		t.Fatalf("empty query: got %d items, want 3", len(got))
	}
	if got := filterSelectItems(items, "api"); len(got) != 1 || got[0].Key != "key" {
		t.Fatalf("label match: got %+v", got)
	}
	if got := filterSelectItems(items, "GPT"); len(got) != 1 || got[0].Key != "model" {
		t.Fatalf("tag match should be case-insensitive: got %+v", got)
	}
	if got := filterSelectItems(items, "zzz"); len(got) != 0 {
		t.Fatalf("no match: got %d items, want 0", len(got))
	}
}

func TestBuildSelectRowsFitsWidth(t *testing.T) {
	withoutColor(t)
	items := []SelectItem{
		{Key: "url", Label: "LLM Gateway URL", Tag: "https://api.example.com/v1"},
		{Key: "key", Label: "API Key", Tag: "sk-************7889"},
		{Key: "model", Label: "Model", Tag: "gpt-4o"},
	}
	for _, width := range []int{20, 40, 60, 80} {
		rows := buildSelectRows("Settings", items, 0, "", "↑/↓ move · Enter edit · esc close", width)
		for i, r := range rows {
			if w := displayWidthANSI(r); w > width {
				t.Errorf("width %d: row %d is %d cols: %q", width, i, w, r)
			}
		}
		joined := strings.Join(rows, "\n")
		var selected string
		for _, r := range rows {
			if strings.HasPrefix(r, "> ") && strings.Contains(r, "LLM") {
				selected = r
				break
			}
		}
		if selected == "" {
			t.Errorf("width %d: selected row not marked: %q", width, joined)
		}
	}
}

func TestBuildSelectRowsGroupsAndNoMatches(t *testing.T) {
	withoutColor(t)
	items := []SelectItem{
		{Key: "a", Label: "Alpha", Group: "Recent"},
		{Key: "b", Label: "Beta", Group: "OpenCode"},
	}
	rows := buildSelectRows("Select model", items, 1, "", "esc", 60)
	joined := strings.Join(rows, "\n")
	for _, want := range []string{"Recent", "OpenCode", "> Beta"} {
		if !strings.Contains(joined, want) {
			t.Errorf("rows missing %q:\n%s", want, joined)
		}
	}

	rows = buildSelectRows("Select model", nil, 0, "zzz", "esc", 60)
	if !strings.Contains(strings.Join(rows, "\n"), "(no matches)") {
		t.Errorf("expected no-matches hint:\n%s", strings.Join(rows, "\n"))
	}
}

func TestBuildSelectRowsWindow(t *testing.T) {
	withoutColor(t)
	items := make([]SelectItem, 12)
	for i := range items {
		items[i] = SelectItem{Key: string(rune('a' + i)), Label: "Item"}
	}
	rows := buildSelectRows("List", items, 11, "", "", 60)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "showing 5-12 of 12") {
		t.Errorf("expected window indicator:\n%s", joined)
	}
	itemRows := 0
	for _, r := range rows {
		if strings.Contains(r, "Item") {
			itemRows++
		}
	}
	if itemRows > selectMaxVisible {
		t.Fatalf("rendered %d item rows, want <= %d", itemRows, selectMaxVisible)
	}
}

func TestSelectRowHighlight(t *testing.T) {
	saved := ColorEnabled()
	SetColorEnabled(true)
	defer SetColorEnabled(saved)

	row := selectRow(SelectItem{Label: "Model", Tag: "gpt"}, true, 30)
	if !strings.Contains(row, "48;5;216") {
		t.Fatalf("selected row should use highlight background: %q", row)
	}
	if w := displayWidthANSI(row); w != 30 {
		t.Fatalf("highlight should span full width: got %d, want 30", w)
	}

	plain := selectRow(SelectItem{Label: "Model", Tag: "gpt"}, false, 30)
	if strings.Contains(plain, "48;5;216") {
		t.Fatalf("unselected row should not be highlighted: %q", plain)
	}
	if !strings.Contains(plain, "Model") || !strings.Contains(plain, "gpt") {
		t.Fatalf("unselected row missing content: %q", plain)
	}
}
