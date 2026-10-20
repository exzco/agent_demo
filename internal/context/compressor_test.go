package context

import (
	"strings"
	"testing"

	"agy/internal/llm"
	"agy/internal/types"
)

func toolCall(name string) types.ToolCall {
	return types.ToolCall{ID: "call_" + name, Function: types.FunctionCall{Name: name}}
}

func TestBuildCompactedKeepsRecentUserMessagesAndSummary(t *testing.T) {
	c := NewCompressor(CompressorConfig{AutoCompactTokenLimit: 1000, KeepRecentUserTokens: 100000})

	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "first"},
		{Role: "assistant", ToolCalls: []types.ToolCall{toolCall("run_command")}},
		{Role: "tool", Content: "out1"},
		{Role: "user", Content: "second"},
		{Role: "assistant", ToolCalls: []types.ToolCall{toolCall("read_file")}},
		{Role: "tool", Content: "out2"},
	}

	out, kept := c.BuildCompacted("sys", messages, "handoff summary")

	// system + the two user messages + the summary message.
	if len(out) != 4 {
		t.Fatalf("expected 4 messages, got %d: %+v", len(out), out)
	}
	if out[0].Role != "system" || out[0].Content != "sys" {
		t.Fatalf("system prompt not preserved: %+v", out[0])
	}
	last := out[len(out)-1]
	if last.Role != "user" || !strings.HasPrefix(last.Content, SummaryPrefix) || !strings.Contains(last.Content, "handoff summary") {
		t.Fatalf("summary bridge malformed: %+v", last)
	}
	// No tool messages may survive: orphaned tool results break the gateway.
	for _, m := range out {
		if m.Role == "tool" {
			t.Fatalf("orphaned tool message in compacted history: %+v", out)
		}
	}
	if len(kept) != 2 || kept[0] != "first" || kept[1] != "second" {
		t.Fatalf("kept user messages mismatch: %+v", kept)
	}
}

func TestBuildCompactedRespectsUserBudget(t *testing.T) {
	// Budget only fits one short message beyond the envelope overhead.
	c := NewCompressor(CompressorConfig{AutoCompactTokenLimit: 1000, KeepRecentUserTokens: 10})

	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "old message that should fall outside the budget"},
		{Role: "user", Content: "new"},
	}

	_, kept := c.BuildCompacted("sys", messages, "summary")
	if len(kept) != 1 || kept[0] != "new" {
		t.Fatalf("expected only the newest message kept, got %+v", kept)
	}
}

func TestBuildCompactedDropsOlderCheckpointSummary(t *testing.T) {
	c := NewCompressor(CompressorConfig{AutoCompactTokenLimit: 1000, KeepRecentUserTokens: 100000})

	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: SummaryPrefix + "\n\nstale summary"},
		{Role: "user", Content: "real question"},
	}

	_, kept := c.BuildCompacted("sys", messages, "fresh summary")
	for _, k := range kept {
		if strings.Contains(k, "stale summary") {
			t.Fatalf("stale checkpoint summary leaked into kept messages: %+v", kept)
		}
	}
	if len(kept) != 1 || kept[0] != "real question" {
		t.Fatalf("expected only the real question kept, got %+v", kept)
	}
}

func TestNeedsCompactionUsesEstimateAndReportedTokens(t *testing.T) {
	c := NewCompressor(CompressorConfig{AutoCompactTokenLimit: 100})

	short := []llm.ChatMessage{{Role: "user", Content: "hi"}}
	if c.NeedsCompaction(short, nil, 0) {
		t.Fatalf("short history must not trigger compaction")
	}
	// Gateway-reported footprint is authoritative even when our estimate is small.
	if !c.NeedsCompaction(short, nil, 100) {
		t.Fatalf("reported tokens at the limit must trigger compaction")
	}
}

func TestDeterministicSummaryCapturesToolActivity(t *testing.T) {
	c := NewCompressor(CompressorConfig{})
	messages := []llm.ChatMessage{
		{Role: "user", Content: "do the thing"},
		{Role: "assistant", ToolCalls: []types.ToolCall{toolCall("run_command")}},
		{Role: "tool", Content: "command output here"},
	}

	summary := c.DeterministicSummary(messages)
	if !strings.Contains(summary, "run_command") {
		t.Fatalf("summary misses tool call: %s", summary)
	}
	if !strings.Contains(summary, "command output here") {
		t.Fatalf("summary misses tool output: %s", summary)
	}
}
