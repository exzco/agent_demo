package context

import (
	"testing"

	"agy/internal/llm"
	"agy/internal/types"
)

func TestApproxTokensASCII(t *testing.T) {
	// 8 ASCII chars ≈ 2 tokens (4 bytes per token, rounded up).
	if got := ApproxTokens("abcdefgh"); got != 2 {
		t.Fatalf("ASCII estimate: got %d, want 2", got)
	}
	if got := ApproxTokens(""); got != 0 {
		t.Fatalf("empty estimate: got %d, want 0", got)
	}
	if got := ApproxTokens("abcde"); got != 2 {
		t.Fatalf("rounding: got %d, want 2", got)
	}
}

func TestApproxTokensNonASCII(t *testing.T) {
	// CJK characters cost roughly one token each.
	if got := ApproxTokens("你好世界"); got != 4 {
		t.Fatalf("CJK estimate: got %d, want 4", got)
	}
	if got := ApproxTokens("hi你好"); got != 3 {
		t.Fatalf("mixed estimate: got %d, want 3", got)
	}
}

func TestCountMessagesTokensIncludesToolsAndReasoning(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "system", Content: "abcdefgh"},
		{
			Role:             "assistant",
			ReasoningContent: "你好",
			ToolCalls: []types.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: types.FunctionCall{
						Name:      "run_command",
						Arguments: `{"CommandLine":"pwd"}`,
					},
				},
			},
		},
	}
	got := CountMessagesTokens(msgs)
	// system: 4 overhead + 2 role ("system") + 2 content
	// assistant: 4 overhead + 3 role ("assistant") + 2 reasoning
	// call: 2 id + 3 name + 6 args (21 bytes/4, rounded up)
	want := 8 + 9 + 11
	if got != want {
		t.Fatalf("CountMessagesTokens: got %d, want %d", got, want)
	}
}

func TestCountRequestTokensAddsToolSchemas(t *testing.T) {
	msgs := []llm.ChatMessage{{Role: "user", Content: "abcdefgh"}}
	tools := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "run_command",
				"description": "Runs a shell command",
				"parameters":  map[string]any{"type": "object"},
			},
		},
	}
	withTools := CountRequestTokens(msgs, tools)
	withoutTools := CountRequestTokens(msgs, nil)
	if withTools <= withoutTools {
		t.Fatalf("tool schemas must add tokens: with=%d without=%d", withTools, withoutTools)
	}
	if CountToolsTokens(nil) != 0 {
		t.Fatalf("nil tools must estimate 0")
	}
}
