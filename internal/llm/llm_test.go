package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestConfigWithFallback(t *testing.T) {
	env := Config{BaseURL: "https://env", WireProtocol: "openai", APIKey: "env-key", Model: "env-model"}
	file := Config{BaseURL: "https://file", APIKey: "file-key", Model: "file-model"}

	got := env.WithFallback(file)
	if got.BaseURL != "https://env" || got.APIKey != "env-key" || got.Model != "env-model" {
		t.Fatalf("env values must win: %+v", got)
	}

	got = (Config{WireProtocol: "openai"}).WithFallback(file)
	if got.BaseURL != "https://file" || got.APIKey != "file-key" || got.Model != "file-model" {
		t.Fatalf("file values must fill gaps: %+v", got)
	}
}

func TestClientSetConfig(t *testing.T) {
	c := NewClient(Config{BaseURL: "https://one", Model: "m1", APIKey: "k1"}, nil)
	c.SetConfig(Config{BaseURL: "https://two", Model: "m2", APIKey: "k2"})
	got := c.Config()
	if got.BaseURL != "https://two" || got.Model != "m2" || got.APIKey != "k2" {
		t.Fatalf("SetConfig did not apply: %+v", got)
	}
}

type captureDoer struct {
	url  string
	auth string
	body []byte
}

func (d *captureDoer) Do(req *http.Request) (*http.Response, error) {
	d.url = req.URL.String()
	d.auth = req.Header.Get("Authorization")
	d.body, _ = io.ReadAll(req.Body)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
		Header:     http.Header{},
	}, nil
}

type sseDoer struct{ response string }

func (d *sseDoer) Do(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(d.response)),
		Header:     http.Header{},
	}, nil
}

// errAfterReader serves the buffered SSE bytes and then fails, emulating a
// stream interrupted mid-flight (Ctrl+C or a dropped connection).
type errAfterReader struct {
	data []byte
	err  error
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func (r *errAfterReader) Close() error { return nil }

type interruptedDoer struct {
	data []byte
	err  error
}

func (d *interruptedDoer) Do(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       &errAfterReader{data: d.data, err: d.err},
		Header:     http.Header{},
	}, nil
}

func TestChatStreamReturnsPartialMessageOnInterrupt(t *testing.T) {
	// When the stream dies before [DONE], the content already received (and
	// any usage chunk that did arrive) must be returned with the error so the
	// caller can still account for the tokens consumed.
	partial := "data: {\"choices\":[{\"delta\":{\"content\":\"partial \"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":40,\"completion_tokens\":10,\"total_tokens\":50}}\n\n"
	c := NewClient(Config{BaseURL: "https://x.example.com", Model: "m"},
		&interruptedDoer{data: []byte(partial), err: context.Canceled})

	msg, err := c.ChatStream(context.Background(), ChatRequest{}, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err: got %v, want context.Canceled", err)
	}
	if msg == nil {
		t.Fatal("partial message must be returned with the error")
	}
	if msg.Content != "partial " {
		t.Fatalf("partial content: got %q", msg.Content)
	}
	if msg.ReasoningContent != "thinking" {
		t.Fatalf("partial reasoning: got %q", msg.ReasoningContent)
	}
	if msg.Usage == nil || msg.Usage.TotalTokens != 50 {
		t.Fatalf("usage received before the interrupt must be preserved: %+v", msg.Usage)
	}
}

func TestChatStreamParsesReasoningContent(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"reasoning_content":"Let me "}}]}`,
		`data: {"choices":[{"delta":{"reasoning_content":"think."}}]}`,
		`data: {"choices":[{"delta":{"content":"Answer."}}]}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	c := NewClient(Config{BaseURL: "https://x.example.com", Model: "m"}, &sseDoer{response: sse})
	var streamed, streamedReasoning string
	msg, err := c.ChatStream(context.Background(), ChatRequest{},
		func(chunk string) { streamed += chunk },
		func(delta string) { streamedReasoning += delta })
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if msg.ReasoningContent != "Let me think." {
		t.Fatalf("reasoning_content: got %q", msg.ReasoningContent)
	}
	if msg.Content != "Answer." {
		t.Fatalf("content: got %q", msg.Content)
	}
	if streamed != "Answer." {
		t.Fatalf("reasoning must not leak into the token stream: got %q", streamed)
	}
	if streamedReasoning != "Let me think." {
		t.Fatalf("reasoning deltas: got %q", streamedReasoning)
	}
}

func TestChatStreamDeduplicatesToolCallFragments(t *testing.T) {
	// Some gateways repeat the full function object per chunk (name included)
	// or stream cumulative arguments; both must not corrupt the final call.
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"run_command","arguments":"{\"Comm"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"run_command","arguments":"{\"CommandLine\":\"pwd\"}"}}]}}]}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	c := NewClient(Config{BaseURL: "https://x.example.com", Model: "m"}, &sseDoer{response: sse})
	msg, err := c.ChatStream(context.Background(), ChatRequest{}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("tool calls: got %d", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Function.Name != "run_command" {
		t.Fatalf("doubled tool name: %q", msg.ToolCalls[0].Function.Name)
	}
	if msg.ToolCalls[0].Function.Arguments != `{"CommandLine":"pwd"}` {
		t.Fatalf("arguments: %q", msg.ToolCalls[0].Function.Arguments)
	}
}

func TestChatStreamAppendsToolCallArgumentDeltas(t *testing.T) {
	// OpenAI-style delta streaming must keep working.
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"run_command","arguments":"{\"Comm"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"andLine\":\"pwd\"}"}}]}}]}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	c := NewClient(Config{BaseURL: "https://x.example.com", Model: "m"}, &sseDoer{response: sse})
	msg, err := c.ChatStream(context.Background(), ChatRequest{}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if msg.ToolCalls[0].Function.Name != "run_command" {
		t.Fatalf("tool name: %q", msg.ToolCalls[0].Function.Name)
	}
	if msg.ToolCalls[0].Function.Arguments != `{"CommandLine":"pwd"}` {
		t.Fatalf("arguments: %q", msg.ToolCalls[0].Function.Arguments)
	}
}

func TestChatMessageMarshalsReasoningContent(t *testing.T) {
	withReasoning, _ := json.Marshal(ChatRequest{Messages: []ChatMessage{
		{Role: "assistant", Content: "c", ReasoningContent: "r"},
	}})
	if !strings.Contains(string(withReasoning), `"reasoning_content":"r"`) {
		t.Fatalf("reasoning_content must be sent back: %s", withReasoning)
	}

	without, _ := json.Marshal(ChatRequest{Messages: []ChatMessage{
		{Role: "assistant", Content: "c"},
	}})
	if strings.Contains(string(without), "reasoning_content") {
		t.Fatalf("empty reasoning_content must be omitted: %s", without)
	}
}

func TestChatStreamUsesUpdatedConfig(t *testing.T) {
	doer := &captureDoer{}
	c := NewClient(Config{BaseURL: "https://old.example.com", Model: "old-model", APIKey: "old-key"}, doer)

	c.SetConfig(Config{BaseURL: "https://new.example.com", Model: "new-model", APIKey: "new-key"})
	if _, err := c.ChatStream(context.Background(), ChatRequest{}, nil, nil); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	if doer.url != "https://new.example.com/v1/chat/completions" {
		t.Fatalf("url: got %s", doer.url)
	}
	if doer.auth != "Bearer new-key" {
		t.Fatalf("auth: got %q", doer.auth)
	}
	var req ChatRequest
	if err := json.Unmarshal(doer.body, &req); err != nil {
		t.Fatalf("body: %v", err)
	}
	if req.Model != "new-model" {
		t.Fatalf("model: got %q", req.Model)
	}
}

func TestChatStreamMergesToolCallsByWireIndex(t *testing.T) {
	// DeepSeek streams every call's fragments in its own chunk at array
	// position 0. Fragments must merge by the wire index, not by position,
	// otherwise the second call's arguments get appended onto the first and
	// the accumulated string becomes "{...}{...}".
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"run_command","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"CommandLine\": \"pwd\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"run_command","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"CommandLine\": \"hostname\"}"}}]}}]}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	c := NewClient(Config{BaseURL: "https://x.example.com", Model: "m"}, &sseDoer{response: sse})
	msg, err := c.ChatStream(context.Background(), ChatRequest{}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if len(msg.ToolCalls) != 2 {
		t.Fatalf("tool calls: got %d, want 2", len(msg.ToolCalls))
	}
	if got := msg.ToolCalls[0].Function.Arguments; got != `{"CommandLine": "pwd"}` {
		t.Fatalf("call 0 arguments: %q", got)
	}
	if got := msg.ToolCalls[1].Function.Arguments; got != `{"CommandLine": "hostname"}` {
		t.Fatalf("call 1 arguments: %q", got)
	}
	if msg.ToolCalls[1].ID != "call_b" {
		t.Fatalf("call 1 id: %q", msg.ToolCalls[1].ID)
	}
}

func TestChatStreamKeepsRepeatedArgumentTailFragment(t *testing.T) {
	// A delta fragment may legitimately equal the tail of the accumulated
	// string (the closing quote right after the escaped-quote fragment in
	// this command); such repeats must be appended, never skipped.
	frags := []string{
		`{"CommandLine": `,
		`"python3 -c `,
		`\"`,
		`print({'a':1})`,
		`\"`,
		`"`,
		`}`,
	}
	var lines []string
	for _, f := range frags {
		chunk := map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{"tool_calls": []any{map[string]any{
				"index":    0,
				"function": map[string]any{"arguments": f},
			}}},
		}}}
		b, _ := json.Marshal(chunk)
		lines = append(lines, "data: "+string(b))
	}
	lines = append(lines, "data: [DONE]", "")
	sse := strings.Join(lines, "\n\n")

	c := NewClient(Config{BaseURL: "https://x.example.com", Model: "m"}, &sseDoer{response: sse})
	msg, err := c.ChatStream(context.Background(), ChatRequest{}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("tool calls: got %d", len(msg.ToolCalls))
	}
	want := `{"CommandLine": "python3 -c \"print({'a':1})\""}`
	if got := msg.ToolCalls[0].Function.Arguments; got != want {
		t.Fatalf("arguments: %q, want %q", got, want)
	}
}

func TestRecoverToolArguments(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"CommandLine":"pwd"}`, `{"CommandLine":"pwd"}`},
		{`{"CommandLine":"pwd"}{"CommandLine":"hostname"}`, `{"CommandLine":"hostname"}`},
		{`{"CommandLine":"pwd"}}`, `{"CommandLine":"pwd"}`},
		{``, ``},
		{`not json at all`, `not json at all`},
	}
	for _, c := range cases {
		if got := recoverToolArguments(c.in); got != c.want {
			t.Fatalf("recoverToolArguments(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestChatStreamParsesUsageChunk(t *testing.T) {
	// Gateways emit a final usage-only chunk before [DONE] when the request
	// sets stream_options.include_usage.
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Done."}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	c := NewClient(Config{BaseURL: "https://x.example.com", Model: "m"}, &sseDoer{response: sse})
	msg, err := c.ChatStream(context.Background(), ChatRequest{}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if msg.Usage == nil {
		t.Fatal("usage chunk not parsed")
	}
	if msg.Usage.PromptTokens != 100 || msg.Usage.CompletionTokens != 50 || msg.Usage.TotalTokens != 150 {
		t.Fatalf("usage: %+v", msg.Usage)
	}
}

func TestChatStreamRequestsUsage(t *testing.T) {
	doer := &captureDoer{}
	c := NewClient(Config{BaseURL: "https://x.example.com", Model: "m"}, doer)
	if _, err := c.ChatStream(context.Background(), ChatRequest{}, nil, nil); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var req ChatRequest
	if err := json.Unmarshal(doer.body, &req); err != nil {
		t.Fatalf("body: %v", err)
	}
	if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
		t.Fatalf("stream_options.include_usage not requested: %s", doer.body)
	}
}

func TestChatStreamParsesDeepSeekCacheUsage(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Done."}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":50,"total_tokens":1050,"prompt_cache_hit_tokens":950,"prompt_cache_miss_tokens":50}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	c := NewClient(Config{BaseURL: "https://x.example.com", Model: "deepseek-chat"}, &sseDoer{response: sse})
	msg, err := c.ChatStream(context.Background(), ChatRequest{}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if msg.Usage == nil {
		t.Fatal("usage chunk not parsed")
	}
	if msg.Usage.PromptCacheHitTokens != 950 || msg.Usage.PromptCacheMissTokens != 50 {
		t.Fatalf("cache tokens: got hit=%d miss=%d, want 950/50", msg.Usage.PromptCacheHitTokens, msg.Usage.PromptCacheMissTokens)
	}
	if msg.Usage.CacheHitTokens() != 950 {
		t.Fatalf("CacheHitTokens() method: got %d, want 950", msg.Usage.CacheHitTokens())
	}
}

func TestChatStreamParsesOpenAICacheUsage(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Done."}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":2000,"completion_tokens":100,"total_tokens":2100,"prompt_tokens_details":{"cached_tokens":1800}}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	c := NewClient(Config{BaseURL: "https://x.example.com", Model: "gpt-4o"}, &sseDoer{response: sse})
	msg, err := c.ChatStream(context.Background(), ChatRequest{}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if msg.Usage == nil {
		t.Fatal("usage chunk not parsed")
	}
	if msg.Usage.PromptCacheHitTokens != 1800 {
		t.Fatalf("OpenAI cached tokens not normalized: got %d, want 1800", msg.Usage.PromptCacheHitTokens)
	}
	if msg.Usage.PromptCacheMissTokens != 200 {
		t.Fatalf("OpenAI miss tokens not normalized: got %d, want 200", msg.Usage.PromptCacheMissTokens)
	}
	if msg.Usage.CacheHitTokens() != 1800 {
		t.Fatalf("CacheHitTokens() method: got %d, want 1800", msg.Usage.CacheHitTokens())
	}
}

