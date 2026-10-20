package llm

import (
	"agy/internal/types"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

type Config struct {
	BaseURL      string
	WireProtocol string
	APIKey       string
	Model        string
	Models       []string
}

// WithFallback fills empty fields with values from fallback (typically a
// persisted settings file). Existing non-empty values always win, so real
// environment variables keep precedence over the config file.
func (cfg Config) WithFallback(fallback Config) Config {
	if cfg.BaseURL == "" {
		cfg.BaseURL = fallback.BaseURL
	}
	if cfg.APIKey == "" {
		cfg.APIKey = fallback.APIKey
	}
	if cfg.Model == "" {
		cfg.Model = fallback.Model
	}
	if len(cfg.Models) == 0 {
		cfg.Models = fallback.Models
	}
	if cfg.WireProtocol == "" {
		cfg.WireProtocol = fallback.WireProtocol
	}
	return cfg
}

func ConfigFromEnv() Config {
	url := strings.TrimSpace(os.Getenv("AGY_LLM_GATEWAY_URL"))
	proto := strings.ToLower(strings.TrimSpace(os.Getenv("AGY_LLM_GATEWAY_WIRE_PROTOCOL")))
	if proto == "" {
		proto = "openai"
	}
	key := os.Getenv("AGY_LLM_GATEWAY_API_KEY")
	model := strings.TrimSpace(os.Getenv("AGY_LLM_GATEWAY_MODEL"))

	var models []string
	if m := os.Getenv("AGY_LLM_GATEWAY_MODELS"); m != "" {
		for _, part := range strings.Split(m, ",") {
			if part = strings.TrimSpace(part); part != "" {
				models = append(models, part)
			}
		}
	}
	if model == "" && len(models) > 0 {
		model = models[0]
	}

	return Config{
		BaseURL:      url,
		WireProtocol: proto,
		APIKey:       key,
		Model:        model,
		Models:       models,
	}
}

type Client struct {
	mu   sync.RWMutex
	cfg  Config
	doer Doer
}

func NewClient(cfg Config, doer Doer) *Client {
	if doer == nil {
		doer = &http.Client{Timeout: 120 * time.Second}
	}
	return &Client{cfg: cfg, doer: doer}
}

// SetConfig hot-swaps the client configuration (gateway URL, API key, model)
// so changes made at runtime by /settings or /model take effect immediately.
func (c *Client) SetConfig(cfg Config) {
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
}

// Config returns a snapshot of the current client configuration.
func (c *Client) Config() Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg
}

type ChatMessage struct {
	Role             string           `json:"role"`
	Content          string           `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ToolCalls        []types.ToolCall `json:"tool_calls,omitempty"`

	// Usage carries the gateway-reported token accounting of the response
	// that produced this assistant message. It is response-only metadata:
	// json:"-" keeps it out of replayed request payloads.
	Usage *Usage `json:"-"`
}

type ChatRequest struct {
	Model         string           `json:"model"`
	Messages      []ChatMessage    `json:"messages"`
	Stream        bool             `json:"stream"`
	Tools         []map[string]any `json:"tools,omitempty"`
	StreamOptions *StreamOptions   `json:"stream_options,omitempty"`
}

// StreamOptions asks OpenAI-compatible gateways to emit a final SSE chunk
// carrying token usage (prompt/completion/total).
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// Usage is the token accounting reported by the gateway.
type Usage struct {
	PromptTokens          int                  `json:"prompt_tokens"`
	CompletionTokens      int                  `json:"completion_tokens"`
	TotalTokens           int                  `json:"total_tokens"`
	PromptCacheHitTokens  int                  `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens int                  `json:"prompt_cache_miss_tokens,omitempty"`
	PromptTokensDetails   *PromptTokensDetails `json:"prompt_tokens_details,omitempty"`
}

type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
}

// CacheHitTokens returns the number of prompt tokens served from prefix cache.
func (u *Usage) CacheHitTokens() int {
	if u == nil {
		return 0
	}
	if u.PromptCacheHitTokens > 0 {
		return u.PromptCacheHitTokens
	}
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > 0 {
		return u.PromptTokensDetails.CachedTokens
	}
	return 0
}

// ToolCallDelta is the streaming fragment shape of one tool call. Index pins
// fragments to their call even when a chunk carries a single entry (DeepSeek
// streams every call's fragments in its own chunk at array position 0).
type ToolCallDelta struct {
	Index    int                `json:"index"`
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function types.FunctionCall `json:"function"`
}

type StreamDelta struct {
	Role             string          `json:"role,omitempty"`
	Content          string          `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCallDelta `json:"tool_calls,omitempty"`
}

type StreamChoice struct {
	Delta StreamDelta `json:"delta"`
}

type StreamChunk struct {
	Choices []StreamChoice `json:"choices"`
	Usage   *Usage         `json:"usage,omitempty"`
}

// ChatStream sends a streaming chat completion request. onChunk receives
// answer content deltas; onReasoning receives reasoning_content deltas from
// thinking-mode gateways (either callback may be nil).
func (c *Client) ChatStream(ctx context.Context, req ChatRequest, onChunk func(string), onReasoning func(string)) (*ChatMessage, error) {
	cfg := c.Config()
	req.Stream = true
	if req.Model == "" {
		req.Model = cfg.Model
	}
	if req.StreamOptions == nil {
		req.StreamOptions = &StreamOptions{IncludeUsage: true}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := cfg.BaseURL
	if !strings.HasSuffix(url, "/chat/completions") {
		url = strings.TrimSuffix(url, "/")
		if strings.HasSuffix(url, "/v1") {
			url = url + "/chat/completions"
		} else {
			url = url + "/v1/chat/completions"
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	resp, err := c.doer.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("model stream error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("model stream error: gateway returned %d: %s", resp.StatusCode, string(b))
	}

	scanner := bufio.NewScanner(resp.Body)
	var fullText strings.Builder
	var fullReasoning strings.Builder
	var usage *Usage
	toolCallsMap := make(map[int]*types.ToolCall)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		// Gateways emit the usage chunk right before [DONE].
		if chunk.Usage != nil {
			usage = chunk.Usage
			if usage.PromptCacheHitTokens == 0 && usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens > 0 {
				usage.PromptCacheHitTokens = usage.PromptTokensDetails.CachedTokens
			}
			if usage.PromptCacheMissTokens == 0 && usage.PromptTokens > 0 && usage.PromptCacheHitTokens > 0 {
				if usage.PromptTokens >= usage.PromptCacheHitTokens {
					usage.PromptCacheMissTokens = usage.PromptTokens - usage.PromptCacheHitTokens
				}
			}
		}

		for _, choice := range chunk.Choices {
			// Thinking-mode gateways stream reasoning_content next to the
			// answer; it must be echoed back verbatim on later turns.
			if choice.Delta.ReasoningContent != "" {
				fullReasoning.WriteString(choice.Delta.ReasoningContent)
				if onReasoning != nil {
					onReasoning(choice.Delta.ReasoningContent)
				}
			}
			if choice.Delta.Content != "" {
				fullText.WriteString(choice.Delta.Content)
				if onChunk != nil {
					onChunk(choice.Delta.Content)
				}
			}
			for _, tc := range choice.Delta.ToolCalls {
				// Fragments are keyed by the wire index, never by array
				// position: a later call's fragments may arrive alone in a
				// chunk at position 0 and would otherwise merge into call 0.
				existing, ok := toolCallsMap[tc.Index]
				if !ok {
					call := types.ToolCall{ID: tc.ID, Type: tc.Type, Function: tc.Function}
					toolCallsMap[tc.Index] = &call
					continue
				}
				if tc.ID != "" {
					existing.ID = tc.ID
				}
				if tc.Type != "" {
					existing.Type = tc.Type
				}
				// Gateways differ in how tool-call fragments stream: OpenAI
				// sends the name once and arguments as deltas, others repeat
				// the full function object or send cumulative strings. Delta
				// fragments are appended, cumulative ones replace the
				// prefix, exact repeats fold into the prefix case.
				if name := tc.Function.Name; name != "" {
					switch {
					case existing.Function.Name == "":
						existing.Function.Name = name
					case strings.HasPrefix(name, existing.Function.Name):
						existing.Function.Name = name
					default:
						existing.Function.Name += name
					}
				}
				if arg := tc.Function.Arguments; arg != "" {
					switch {
					case existing.Function.Arguments == "":
						existing.Function.Arguments = arg
					case strings.HasPrefix(arg, existing.Function.Arguments):
						existing.Function.Arguments = arg // cumulative stream
					default:
						existing.Function.Arguments += arg // delta stream
					}
				}
			}
		}
	}

	// Emit calls ordered by their wire index so multi-call turns replay in
	// the order the model planned them.
	idxs := make([]int, 0, len(toolCallsMap))
	for i := range toolCallsMap {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)

	var toolCalls []types.ToolCall
	for _, i := range idxs {
		tc := toolCallsMap[i]
		tc.Function.Arguments = recoverToolArguments(tc.Function.Arguments)
		toolCalls = append(toolCalls, *tc)
	}

	msg := &ChatMessage{
		Role:             "assistant",
		Content:          fullText.String(),
		ReasoningContent: fullReasoning.String(),
		ToolCalls:        toolCalls,
		Usage:            usage,
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		// Return the partial message together with the error: an interrupted
		// stream (Ctrl+C, network reset) was still billed for its prompt and
		// whatever content arrived before the failure. Callers use it for
		// token accounting instead of losing the whole call.
		return msg, fmt.Errorf("model stream error: %w", err)
	}

	return msg, nil
}

// recoverToolArguments salvages a tool call's arguments when a corrupted
// stream left them unparseable (e.g. concatenated objects "{...}{..." or a
// duplicated closing brace): the last complete JSON object wins. Valid JSON
// and unsalvageable strings are returned unchanged.
func recoverToolArguments(s string) string {
	if s == "" || json.Valid([]byte(s)) {
		return s
	}
	dec := json.NewDecoder(strings.NewReader(s))
	var last json.RawMessage
	for {
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			break
		}
		if len(v) > 0 && v[0] == '{' {
			last = v
		}
	}
	if len(last) > 0 {
		return string(last)
	}
	return s
}
