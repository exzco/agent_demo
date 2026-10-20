package context

import (
	"fmt"
	"strings"

	"agy/internal/llm"
	"agy/internal/trajectory"
	"agy/internal/types"
)

// SummarizationPrompt is the handoff-summary instruction sent to the model
// when the context budget is exceeded. It mirrors Codex's SUMMARIZATION_PROMPT
// (codex-rs/prompts/templates/compact/prompt.md) so the replacement history
// keeps the same shape: progress, constraints, next steps, key data.
const SummarizationPrompt = `You are performing a CONTEXT CHECKPOINT COMPACTION. Do not call any tools. Output only the structured handoff summary text.

Include:
- Current progress and key decisions made
- Important context, constraints, or user preferences
- What remains to be done (clear next steps)
- Any critical data, examples, or references needed to continue

Be concise, structured, and focused on helping the next LLM seamlessly continue the work.`

// SummaryPrefix heads the compacted summary message, mirroring Codex's
// SUMMARY_PREFIX (codex-rs/prompts/templates/compact/summary_prefix.md).
const SummaryPrefix = "Another language model started to solve this problem and produced a summary. " +
	"Use this to build on the work that has already been done and avoid duplicating work. " +
	"Here is the summary produced by the other language model:"

const (
	// DefaultAutoCompactTokenLimit is the fallback compaction threshold when
	// neither settings nor the agent config specify one (80% of 1M context).
	DefaultAutoCompactTokenLimit = 800000
	// DefaultKeepRecentUserTokens caps the user messages carried across a
	// checkpoint, like Codex's COMPACT_USER_MESSAGE_MAX_TOKENS.
	DefaultKeepRecentUserTokens = 20000
)

type CompressorConfig struct {
	// AutoCompactTokenLimit triggers compaction once the estimated (or
	// gateway-reported) conversation size reaches it.
	AutoCompactTokenLimit int
	// KeepRecentUserTokens is the budget for user messages preserved across
	// a compaction boundary.
	KeepRecentUserTokens int
}

type Compressor struct {
	cfg CompressorConfig
}

func NewCompressor(cfg CompressorConfig) *Compressor {
	if cfg.AutoCompactTokenLimit <= 0 {
		cfg.AutoCompactTokenLimit = DefaultAutoCompactTokenLimit
	}
	if cfg.KeepRecentUserTokens <= 0 {
		cfg.KeepRecentUserTokens = DefaultKeepRecentUserTokens
	}
	return &Compressor{cfg: cfg}
}

// TokenLimit returns the effective auto-compaction threshold.
func (c *Compressor) TokenLimit() int { return c.cfg.AutoCompactTokenLimit }

// SetTokenLimit updates the auto-compaction threshold at runtime (model or
// context-window switch). Non-positive values restore the default.
func (c *Compressor) SetTokenLimit(limit int) {
	if limit <= 0 {
		limit = DefaultAutoCompactTokenLimit
	}
	c.cfg.AutoCompactTokenLimit = limit
}

// NeedsCompaction reports whether the conversation has outgrown the token
// budget. reportedTokens is the gateway-reported context footprint when
// available (authoritative over the local estimate).
func (c *Compressor) NeedsCompaction(messages []llm.ChatMessage, tools []map[string]any, reportedTokens int) bool {
	tokens := CountRequestTokens(messages, tools)
	if reportedTokens > tokens {
		tokens = reportedTokens
	}
	return tokens >= c.cfg.AutoCompactTokenLimit
}

// BuildCompacted builds the replacement history after a checkpoint: the
// system prompt, a bounded tail of recent user messages (most recent first
// while budget remains), and the summary as the final user message. Tool
// results and assistant tool-call payloads are intentionally dropped: they
// are represented by the summary, which keeps the rebuilt history free of
// orphaned tool messages.
func (c *Compressor) BuildCompacted(systemPrompt string, messages []llm.ChatMessage, summary string) ([]llm.ChatMessage, []string) {
	remaining := c.cfg.KeepRecentUserTokens
	var kept []llm.ChatMessage
	for i := len(messages) - 1; i >= 0 && remaining > 0; i-- {
		m := messages[i]
		if m.Role != "user" {
			continue
		}
		if strings.HasPrefix(m.Content, SummaryPrefix) {
			// A previous checkpoint summary; the newest summary supersedes it.
			continue
		}
		if remaining <= messageOverhead+1 {
			// No room for a meaningful message beyond the envelope.
			break
		}
		t := CountMessagesTokens([]llm.ChatMessage{m})
		if t > remaining {
			m.Content = truncateToTokens(m.Content, remaining-messageOverhead)
			t = CountMessagesTokens([]llm.ChatMessage{m})
			if t > remaining {
				break
			}
		}
		kept = append([]llm.ChatMessage{m}, kept...)
		remaining -= t
	}

	out := make([]llm.ChatMessage, 0, len(kept)+2)
	out = append(out, llm.ChatMessage{Role: "system", Content: systemPrompt})
	out = append(out, kept...)
	out = append(out, llm.ChatMessage{Role: "user", Content: SummaryPrefix + "\n\n" + summary})

	keptTexts := make([]string, 0, len(kept))
	for _, m := range kept {
		keptTexts = append(keptTexts, m.Content)
	}
	return out, keptTexts
}

// truncateToTokens cuts text down to a budget measured with the same rule as
// ApproxTokens (ASCII 4 bytes/token, non-ASCII 1 token/rune), keeping the
// head of the message and appending an ellipsis.
func truncateToTokens(s string, tokenBudget int) string {
	if tokenBudget <= 0 {
		return ""
	}
	ascii := 0
	nonASCII := 0
	for i, r := range s {
		if r < 128 {
			ascii++
		} else {
			nonASCII++
		}
		if (ascii+3)/4+nonASCII > tokenBudget {
			return s[:i] + "…"
		}
	}
	return s
}

// DeterministicSummary is the offline fallback used when the summarization
// call fails: a compact digest of tool calls and results so the model still
// receives a usable bridge instead of losing the middle of the conversation.
func (c *Compressor) DeterministicSummary(messages []llm.ChatMessage) string {
	var sb strings.Builder
	sb.WriteString("[Deterministic fallback summary]\n")
	for _, m := range messages {
		switch m.Role {
		case "assistant":
			if len(m.ToolCalls) > 0 {
				var calls []string
				for _, tc := range m.ToolCalls {
					calls = append(calls, tc.Function.Name)
				}
				sb.WriteString(fmt.Sprintf("- Model decided to call: %s\n", strings.Join(calls, ", ")))
			} else if m.Content != "" {
				sb.WriteString(fmt.Sprintf("- Model note: %s\n", preview(m.Content, 100)))
			}
		case "tool":
			sb.WriteString(fmt.Sprintf("  - Tool output: %s\n", preview(m.Content, 120)))
		}
	}
	return sb.String()
}

func preview(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// CompactTrajectory generates a summary step for the trajectory.
func (c *Compressor) CompactTrajectory(traj *trajectory.Trajectory) trajectory.Step {
	steps := traj.Steps()
	var details strings.Builder
	details.WriteString("Compacted trajectory summary:\n")
	for i, s := range steps {
		details.WriteString(fmt.Sprintf("[%d] %s: %s\n", i+1, s.Header.StepType, s.Header.Status))
	}

	return trajectory.Step{
		Header: types.StepHeader{
			StepType: types.StepTypeCheckpoint,
			Status:   types.StepStatusDone,
		},
		Payload: details.String(),
	}
}
