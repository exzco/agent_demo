package main

import (
	ccontext "agy/internal/context"
	"agy/internal/customizations"
	"agy/internal/llm"
	"agy/internal/subagent"
	"agy/internal/tools"
	"agy/internal/tui"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubagentManager(t *testing.T) {
	// Mock subagent response
	resp := sseChunk("I am the subagent researcher and here are my findings.", nil)
	mock := &mockDoer{responses: []string{resp}}

	cfg := llm.Config{BaseURL: "http://mock-gw", Model: "mock"}
	client := llm.NewClient(cfg, mock)
	reg := tools.NewRegistry()

	mgr := subagent.NewManager(client, reg)
	tool := subagent.NewInvokeSubagentTool(mgr)

	args, _ := json.Marshal(subagent.InvokeSubagentArgs{
		Subagents: []subagent.SubagentSpec{
			{Role: "Researcher", Prompt: "Find all auth endpoints"},
		},
	})

	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("invoke_subagent failed: %v", err)
	}

	if !strings.Contains(res, "Researcher") || !strings.Contains(res, "findings") {
		t.Errorf("unexpected subagent output: %s", res)
	}

	insts := mgr.List()
	if len(insts) != 1 {
		t.Fatalf("expected 1 subagent instance, got %d", len(insts))
	}
	if insts[0].Role != "Researcher" || insts[0].Status != "DONE" {
		t.Errorf("instance state mismatch: %+v", insts[0])
	}
}

func TestContextCompressor(t *testing.T) {
	compressor := ccontext.NewCompressor(ccontext.CompressorConfig{
		AutoCompactTokenLimit: 100,
		KeepRecentUserTokens:  10000,
	})

	msgs := []llm.ChatMessage{
		{Role: "system", Content: "System prompt"},
		{Role: "user", Content: "Initial user prompt"},
		{Role: "assistant", Content: "Plan step 1"},
		{Role: "tool", Content: "Result 1", ToolCallID: "1"},
		{Role: "user", Content: "Recent follow up"},
	}

	compacted, kept := compressor.BuildCompacted("System prompt", msgs, "handoff")
	// Should be: system (1) + kept user messages (2) + summary (1).
	if len(compacted) != 4 {
		t.Fatalf("expected 4 messages, got %d: %+v", len(compacted), compacted)
	}
	if len(kept) != 2 || kept[1] != "Recent follow up" {
		t.Fatalf("expected both user messages kept, got %+v", kept)
	}
	last := compacted[len(compacted)-1]
	if !strings.Contains(last.Content, "handoff") || !strings.Contains(last.Content, ccontext.SummaryPrefix) {
		t.Fatalf("summary bridge malformed: %s", last.Content)
	}
}

func TestCustomizations(t *testing.T) {
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "my-skill")
	os.MkdirAll(skillDir, 0755)

	skillContent := `---
name: Code Auditor
description: Audits code for security flaws
---
# Instructions
Always check buffer bounds.
`
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0644)
	os.WriteFile(filepath.Join(tmpDir, ".acodeignore"), []byte("secrets/\n*.key\n"), 0644)

	mgr := customizations.NewCustomizationsManager()
	if err := mgr.LoadFromWorkspace(tmpDir); err != nil {
		t.Fatal(err)
	}

	skills := mgr.Skills()
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill discovered, got %d", len(skills))
	}
	if skills[0].Name != "Code Auditor" {
		t.Errorf("skill name mismatch: %s", skills[0].Name)
	}

	promptAug := mgr.GeneratePromptAugmentation()
	if !strings.Contains(promptAug, "Code Auditor") || !strings.Contains(promptAug, "<custom_skills>") {
		t.Errorf("prompt augmentation missing skill info: %s", promptAug)
	}

	// Test ignore filter
	if !mgr.IsIgnored("secrets/passwords.txt") {
		t.Error("expected secrets/passwords.txt to be ignored")
	}
	if mgr.IsIgnored("src/main.go") {
		t.Error("src/main.go should not be ignored")
	}
}

func TestDiffViewer(t *testing.T) {
	oldText := "line 1\nline 2 to delete\nline 3"
	newText := "line 1\nline 2 MODIFIED\nline 3\nline 4 added"

	diff := tui.GenerateUnifiedDiff("test.txt", oldText, newText)
	if !strings.Contains(diff, "line 2 to delete") {
		t.Errorf("missing deleted line in diff: %s", diff)
	}
	if !strings.Contains(diff, "line 2 MODIFIED") {
		t.Errorf("missing modified line in diff: %s", diff)
	}
	if !strings.Contains(diff, "line 4 added") {
		t.Errorf("missing added line in diff: %s", diff)
	}

	app := tui.NewApp("model-1", "http://gw")
	modal := app.RenderDiffModal("test.txt", oldText, newText)
	if !strings.Contains(modal, "Reviewing Diff: test.txt") {
		t.Errorf("modal header mismatch: %s", modal)
	}
}

func TestTUIStatusBar(t *testing.T) {
	app := tui.NewApp("DeepSeek-V3", "https://api.deepseek.com")
	app.TotalSteps = 12
	app.ContextLimit = 128000
	app.ContextTokens = 50000
	bar := app.RenderStatusBar()

	if !strings.Contains(bar, "DeepSeek-V3") || !strings.Contains(bar, "https://api.deepseek.com") {
		t.Errorf("status bar missing metrics: %s", bar)
	}
	if !strings.Contains(bar, "Context: 50,000 tokens 39%") {
		t.Errorf("status bar missing context usage: %s", bar)
	}
	if strings.Contains(bar, "Steps") {
		t.Errorf("status bar should not show step count: %s", bar)
	}
	if strings.Contains(bar, "Perm:") {
		t.Errorf("status bar must hide an empty permission mode: %s", bar)
	}

	// The sandbox mode sits right after the version so it stays visible even
	// on narrow terminals that truncate the end of the bar.
	app.PermissionMode = "workspace"
	bar = app.RenderStatusBar()
	if !strings.Contains(bar, "Perm: workspace") {
		t.Errorf("status bar missing permission mode: %s", bar)
	}
	if strings.Index(bar, "Perm: workspace") > strings.Index(bar, "Context:") {
		t.Errorf("permission mode should precede context usage: %s", bar)
	}

	// Verify no background shadow code \x1b[48;5;236 in status bar
	if strings.Contains(bar, "48;5;236") {
		t.Errorf("status bar should not contain background shadow: %s", bar)
	}

	// Verify prompt cache hit percentage display
	app.CacheHitTokens = 98000
	app.CacheMissTokens = 2000
	bar = app.RenderStatusBar()
	if !strings.Contains(bar, "98% cached") {
		t.Errorf("status bar missing cache percentage: %s", bar)
	}
}
