package context

import (
	"encoding/json"

	"agy/internal/llm"
)

// ApproxTokens estimates the token count of a string without a tokenizer.
// ASCII bytes are charged ~4 per token (English/code average); non-ASCII
// runes (CJK et al.) are charged one token per rune. The estimate is used
// for context-usage display and compaction triggers where a real tokenizer
// would be overkill.
func ApproxTokens(s string) int {
	ascii := 0
	nonASCII := 0
	for _, r := range s {
		if r < 128 {
			ascii++
		} else {
			nonASCII++
		}
	}
	return (ascii+3)/4 + nonASCII
}

// messageOverhead approximates the per-message chat envelope (role markers,
// delimiters, formatting) billed by the gateway on every request.
const messageOverhead = 4

// CountMessagesTokens estimates the tokens of a chat message list, including
// reasoning content and tool-call names/arguments that get replayed on every
// request.
func CountMessagesTokens(messages []llm.ChatMessage) int {
	total := 0
	for _, m := range messages {
		total += messageOverhead
		total += ApproxTokens(m.Role)
		total += ApproxTokens(m.Content)
		total += ApproxTokens(m.ReasoningContent)
		total += ApproxTokens(m.ToolCallID)
		for _, tc := range m.ToolCalls {
			total += ApproxTokens(tc.ID)
			total += ApproxTokens(tc.Function.Name)
			total += ApproxTokens(tc.Function.Arguments)
		}
	}
	return total
}

// CountToolsTokens estimates the tokens of the OpenAI tool schema payload
// that accompanies every request.
func CountToolsTokens(tools []map[string]any) int {
	if len(tools) == 0 {
		return 0
	}
	b, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return ApproxTokens(string(b))
}

// CountRequestTokens estimates the total prompt tokens of a request
// (messages plus tool schemas), mirroring what the gateway bills.
func CountRequestTokens(messages []llm.ChatMessage, tools []map[string]any) int {
	return CountMessagesTokens(messages) + CountToolsTokens(tools)
}
