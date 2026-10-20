package agent

import (
	ccontext "agy/internal/context"
	"agy/internal/hooks"
	"agy/internal/llm"
	"agy/internal/permission"
	"agy/internal/tools"
	"agy/internal/trajectory"
	"agy/internal/types"
	"context"
	"errors"
	"fmt"
	"strings"
)

type EventListener interface {
	OnToken(token string)
	OnReasoning(delta string)
	OnToolCall(call types.ToolCall)
	OnToolResult(call types.ToolCall, result string, err error)
	OnStepComplete(step trajectory.Step)
	OnFinish(reason string)
}

// ToolApprover is an optional interface a listener may implement to gate tool
// execution behind user confirmation (e.g. an interactive y/n diff review).
// Returning (false, nil) rejects the call; the model receives a rejection
// notice and can adapt its plan.
type ToolApprover interface {
	ApproveToolCall(call types.ToolCall) (bool, error)
}

type DefaultEventListener struct{}

func (DefaultEventListener) OnToken(token string)                                    {}
func (DefaultEventListener) OnReasoning(delta string)                                {}
func (DefaultEventListener) OnToolCall(call types.ToolCall)                          {}
func (DefaultEventListener) OnToolResult(call types.ToolCall, res string, err error) {}
func (DefaultEventListener) OnStepComplete(step trajectory.Step)                     {}
func (DefaultEventListener) OnFinish(reason string)                                  {}

type AgentConfig struct {
	SystemPrompt string
	MaxSteps     int
	// AutoCompactTokenLimit is the context budget that triggers a Codex-style
	// summarization checkpoint before the next model request.
	AutoCompactTokenLimit int
	// Permission gates tool execution by sandbox mode (nil = unrestricted,
	// legacy confirm flow only).
	Permission *permission.Gate
}

// DefaultSystemPrompt is the unified root system instruction across main agents and subagents.
// A byte-for-byte identical prefix is strictly required for 100% KV Cache reuse.
const DefaultSystemPrompt = "You are ACode, a powerful AI coding agent. You have access to tools to inspect and modify the environment, as well as invoke subagents for subtasks."

// ProgressProtocolPrompt is appended to the global system prompt (and to
// subagent prompts): periodic progress lines while working, and a completion
// degree line at the end of the task.
const ProgressProtocolPrompt = `Progress protocol (mandatory):
- Work autonomously and do not stop for trivial updates.
- After roughly every 10 tool calls, print exactly one compact progress line in the form: 进度: <done> · <in progress> · <remaining>.
- When the task completes (or you must stop), end your final message with a completion line in the form: 完成度: X% (X is an integer 0-100), followed by a one-line list of anything unfinished (or 无).`

// CompactionListener is implemented by listeners that want to surface
// context-compaction events in the UI.
type CompactionListener interface {
	OnCompaction(beforeTokens, afterTokens int)
}

type Agent struct {
	cfg           AgentConfig
	client        *llm.Client
	registry      *tools.Registry
	runner        *hooks.HookRunner
	listener      EventListener
	compressor    *ccontext.Compressor
	gate          *permission.Gate
	contextTokens int
	// usage is the cumulative token cost of every model call in this agent,
	// including interrupted calls whose final usage chunk never arrived.
	usage llm.Usage
}

func NewAgent(cfg AgentConfig, client *llm.Client, registry *tools.Registry, listener EventListener) *Agent {
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = DefaultSystemPrompt
	}
	if !strings.Contains(cfg.SystemPrompt, ProgressProtocolPrompt) {
		cfg.SystemPrompt += "\n\n" + ProgressProtocolPrompt
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 50
	}
	if listener == nil {
		listener = DefaultEventListener{}
	}

	return &Agent{
		cfg:        cfg,
		client:     client,
		registry:   registry,
		runner:     hooks.NewHookRunner(cfg.MaxSteps),
		listener:   listener,
		compressor: ccontext.NewCompressor(ccontext.CompressorConfig{AutoCompactTokenLimit: cfg.AutoCompactTokenLimit}),
		gate:       cfg.Permission,
	}
}

// ContextTokens returns the token footprint of the conversation after the
// latest model response: the gateway-reported total when available, otherwise
// a local estimate. It drives the context-usage footer.
func (a *Agent) ContextTokens() int {
	return a.contextTokens
}

// UsageTotals returns the cumulative token usage of every model call made by
// this agent. Successful calls use the gateway-reported accounting; calls that
// were interrupted before the final usage chunk arrived are estimated from the
// prompt and the partial stream, so Ctrl+C never erases consumed tokens.
func (a *Agent) UsageTotals() llm.Usage {
	return a.usage
}

// ResetMetrics clears the cumulative token counters and the context footprint
// (used when the conversation is reset with /clear).
func (a *Agent) ResetMetrics() {
	a.usage = llm.Usage{}
	a.contextTokens = 0
}

// SetAutoCompactLimit updates the token budget that triggers automatic
// compaction (used when the active model or context window changes at
// runtime). A non-positive limit falls back to the compressor default.
func (a *Agent) SetAutoCompactLimit(limit int) {
	a.compressor.SetTokenLimit(limit)
}

// Run executes a multi-step task starting from a user prompt.
func (a *Agent) Run(ctx context.Context, traj *trajectory.Trajectory, userPrompt string) (string, error) {
	// 1. Record user input step
	uStep := trajectory.NewUserInputStep(userPrompt)
	traj.AppendStep(uStep)
	a.listener.OnStepComplete(uStep)

	// Build initial messages (checkpoint-aware replay)
	messages := a.buildReplayMessages(traj)

	availableTools := a.registry.ExportOpenAITools()
	contextRetried := false
	// autoCompactArmed disarms when a compaction fails to shrink the
	// conversation, preventing a compaction loop until new content arrives.
	autoCompactArmed := true

	// 2. Multi-turn Agent loop
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		// Codex-style context management: when the conversation outgrows its
		// token budget, or a work segment reaches the step budget, summarize
		// the history into a checkpoint and keep going instead of ending the
		// run with an abrupt "max steps exceeded" stop.
		stepBudgetReached := a.cfg.MaxSteps > 0 &&
			traj.NumCountableStepsSinceLastCheckpoint() >= a.cfg.MaxSteps
		if stepBudgetReached {
			if _, _, cerr := a.compact(ctx, traj, &messages, availableTools); cerr != nil {
				return "", cerr
			}
		} else if autoCompactArmed && a.compressor.NeedsCompaction(messages, availableTools, a.contextTokens) {
			before, after, cerr := a.compact(ctx, traj, &messages, availableTools)
			if cerr != nil {
				return "", cerr
			}
			autoCompactArmed = after < before
		}

		req := llm.ChatRequest{
			Messages: messages,
			Stream:   true,
			Tools:    availableTools,
		}

		// Call LLM with streaming callbacks
		respMsg, err := a.client.ChatStream(ctx, req, func(chunk string) {
			a.listener.OnToken(chunk)
		}, func(delta string) {
			a.listener.OnReasoning(delta)
		})
		if err != nil {
			// A canceled or broken stream still consumed a prompt and any
			// content streamed before the failure. Record both the cumulative
			// usage and the context footprint from the partial response so an
			// interrupt (Ctrl+C) does not silently lose token accounting.
			if a.accountUsage(ctx, req.Messages, respMsg, availableTools, err) {
				a.updateContextUsage(req.Messages, respMsg, availableTools)
			}
			// Mid-turn context overflow: compact once and retry the step,
			// mirroring Codex's ContextWindowExceeded compaction path.
			if !contextRetried && isContextLimitError(err) {
				contextRetried = true
				if _, _, cerr := a.compact(ctx, traj, &messages, availableTools); cerr != nil {
					return "", cerr
				}
				continue
			}
			return "", fmt.Errorf("agent call failed: %w", err)
		}
		a.accountUsage(ctx, req.Messages, respMsg, availableTools, nil)
		a.updateContextUsage(req.Messages, respMsg, availableTools)

		// Record PlannerResponse step
		pStep := trajectory.NewPlannerResponseStepWithReasoning(respMsg.Content, respMsg.ReasoningContent, respMsg.ToolCalls)
		traj.AppendStep(pStep)
		a.listener.OnStepComplete(pStep)

		// Append to local messages
		messages = append(messages, *respMsg)

		// Check hooks after planner response
		stop, reason, err := a.runner.Evaluate(traj, pStep)
		if err != nil {
			return "", err
		}
		if stop {
			if isBoundaryReason(reason) {
				if final := a.wrapUp(ctx, messages, availableTools, reason); final != "" {
					a.listener.OnFinish(reason)
					return final, nil
				}
			}
			a.listener.OnFinish(reason)
			return respMsg.Content, nil
		}

		// Execute tool calls
		for _, call := range respMsg.ToolCalls {
			a.listener.OnToolCall(call)

			var result string
			var toolErr error

			// Sandbox permission gate (read-only / workspace / full). A
			// denial is surfaced as a tool error so the model sees why and
			// can adapt instead of the whole run dying.
			if toolErr == nil && a.gate != nil {
				if gerr := a.gate.Check(call); gerr != nil {
					toolErr = gerr
				}
			}

			if toolErr == nil {
				if approver, ok := a.listener.(ToolApprover); ok {
					approved, aerr := approver.ApproveToolCall(call)
					if aerr != nil {
						// Malformed arguments or an approval I/O fault must
						// not kill the whole run: surface it as a tool error
						// so the model can correct itself on the next step.
						result = ""
						toolErr = fmt.Errorf("tool approval failed: %w", aerr)
					} else if !approved {
						result = ""
						toolErr = fmt.Errorf("tool call rejected by user")
					}
				}
			}

			if toolErr == nil {
				result, toolErr = a.registry.Execute(ctx, call)
			}
			a.listener.OnToolResult(call, result, toolErr)

			tStep := trajectory.NewToolExecutionStep(call.Function.Name, call.ID, result, toolErr)
			tStep.Header.Metadata.ToolCallArguments = call.Function.Arguments
			traj.AppendStep(tStep)
			a.listener.OnStepComplete(tStep)

			// Feed tool output into chat context
			content := result
			if toolErr != nil {
				content = fmt.Sprintf("Error: %s", toolErr.Error())
			}
			messages = append(messages, llm.ChatMessage{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    content,
			})

			// Check hooks after tool execution (e.g. continuation cap, loop detection)
			stop, reason, err := a.runner.Evaluate(traj, tStep)
			if err != nil {
				return "", err
			}
			if stop {
				if isBoundaryReason(reason) {
					if final := a.wrapUp(ctx, messages, availableTools, reason); final != "" {
						a.listener.OnFinish(reason)
						return final, nil
					}
				}
				a.listener.OnFinish(reason)
				return result, nil
			}
		}
	}
}

// buildReplayMessages reconstructs the chat history from the trajectory,
// honoring compaction checkpoints: at a checkpoint the history resets to the
// system prompt, the checkpoint's kept user messages and the handoff summary.
func (a *Agent) buildReplayMessages(traj *trajectory.Trajectory) []llm.ChatMessage {
	messages := []llm.ChatMessage{
		{Role: "system", Content: a.cfg.SystemPrompt},
	}
	reset := func() {
		messages = []llm.ChatMessage{
			{Role: "system", Content: a.cfg.SystemPrompt},
		}
	}
	for _, s := range traj.Steps() {
		switch s.Header.StepType {
		case types.StepTypeCheckpoint:
			p, ok := s.Payload.(trajectory.CheckpointPayload)
			if !ok {
				continue
			}
			reset()
			for _, u := range p.KeptUserMessages {
				messages = append(messages, llm.ChatMessage{Role: "user", Content: u})
			}
			messages = append(messages, llm.ChatMessage{
				Role:    "user",
				Content: ccontext.SummaryPrefix + "\n\n" + p.Summary,
			})
		case types.StepTypeUserInput:
			if p, ok := s.Payload.(trajectory.UserInputPayload); ok {
				messages = append(messages, llm.ChatMessage{Role: "user", Content: p.Text})
			}
		case types.StepTypePlannerResponse:
			if p, ok := s.Payload.(trajectory.PlannerResponsePayload); ok {
				messages = append(messages, llm.ChatMessage{
					Role:             "assistant",
					Content:          p.Text,
					ReasoningContent: p.ReasoningContent,
					ToolCalls:        p.ToolCalls,
				})
			}
		case types.StepTypeToolExecution:
			if p, ok := s.Payload.(trajectory.ToolExecutionPayload); ok {
				res := p.Result
				if p.Error != "" {
					res = fmt.Sprintf("Error: %s", p.Error)
				}
				messages = append(messages, llm.ChatMessage{
					Role:       "tool",
					ToolCallID: p.CallID,
					Content:    res,
				})
			}
		}
	}
	return messages
}

// CompactNow forces a context compaction over the replayed trajectory
// (backing the /compact TUI command). It returns the token footprint before
// and after the checkpoint.
func (a *Agent) CompactNow(ctx context.Context, traj *trajectory.Trajectory) (int, int, error) {
	messages := a.buildReplayMessages(traj)
	tools := a.registry.ExportOpenAITools()
	return a.compact(ctx, traj, &messages, tools)
}

// compact summarizes the conversation into a Codex-style checkpoint, rewrites
// messages to the compacted view, records a checkpoint step on the trajectory
// and notifies the UI. When the summarization call fails it falls back to a
// deterministic digest so the middle of the conversation is never lost.
func (a *Agent) compact(ctx context.Context, traj *trajectory.Trajectory, messages *[]llm.ChatMessage, tools []map[string]any) (int, int, error) {
	before := a.contextTokens
	if before == 0 {
		before = ccontext.CountRequestTokens(*messages, tools)
	}

	summary, err := a.summarize(ctx, *messages, tools)
	if err != nil || strings.TrimSpace(summary) == "" {
		summary = a.compressor.DeterministicSummary(*messages)
	}

	compacted, kept := a.compressor.BuildCompacted(a.cfg.SystemPrompt, *messages, summary)
	after := ccontext.CountRequestTokens(compacted, tools)

	step := trajectory.NewCheckpointStep(summary, kept, before, after)
	traj.AppendStep(step)
	a.listener.OnStepComplete(step)

	*messages = compacted
	a.contextTokens = after
	if cl, ok := a.listener.(CompactionListener); ok {
		cl.OnCompaction(before, after)
	}
	return before, after, nil
}

// summarize asks the model for a handoff summary of the conversation.
// Following DeepSeek Harness's prefix cache reuse architecture, it plays
// back the exact existing conversation prefix (system prompt, tools and history)
// and appends the summarization directive as a final user message.
// This turns the auxiliary summarization request into a near-100% KV cache hit
// instead of reprocessing the whole transcript from scratch.
func (a *Agent) summarize(ctx context.Context, messages []llm.ChatMessage, tools []map[string]any) (string, error) {
	msgs := make([]llm.ChatMessage, 0, len(messages)+1)
	msgs = append(msgs, messages...)
	msgs = append(msgs, llm.ChatMessage{
		Role:    "user",
		Content: ccontext.SummarizationPrompt,
	})

	req := llm.ChatRequest{
		Messages: msgs,
		Stream:   true,
		Tools:    tools,
	}
	resp, err := a.client.ChatStream(ctx, req, func(string) {}, func(string) {})
	a.accountUsage(ctx, req.Messages, resp, tools, err)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

// wrapUpPrompt forces a final summary answer when a boundary hook (step cap
// or loop detection) stops the run mid-task, replacing an abrupt termination.
const wrapUpPrompt = "You have reached the step budget for this task. Do not call any more tools. " +
	"Based on everything gathered so far, produce your final answer now, including what was completed, " +
	"what remains, and end with the completion line: 完成度: X%"

// wrapUp performs one last tool-free model turn so the user receives a final
// answer (with the completion degree) instead of an abrupt cap message.
func (a *Agent) wrapUp(ctx context.Context, messages []llm.ChatMessage, tools []map[string]any, reason string) string {
	msgs := make([]llm.ChatMessage, 0, len(messages)+1)
	msgs = append(msgs, messages...)
	msgs = append(msgs, llm.ChatMessage{Role: "user", Content: wrapUpPrompt})

	req := llm.ChatRequest{
		Messages: msgs,
		Stream:   true,
		Tools:    tools,
	}
	resp, err := a.client.ChatStream(ctx, req, a.listener.OnToken, a.listener.OnReasoning)
	a.accountUsage(ctx, req.Messages, resp, tools, err)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(resp.Content)
	if text == "" {
		return ""
	}
	pStep := trajectory.NewPlannerResponseStepWithReasoning(text, resp.ReasoningContent, nil)
	a.listener.OnStepComplete(pStep)
	return text
}

// isBoundaryReason reports whether a stop reason is a resource boundary where
// a wrap-up turn (final answer instead of an abrupt stop) is appropriate.
// The step budget no longer stops a run: it triggers a compaction checkpoint.
func isBoundaryReason(reason string) bool {
	return strings.Contains(strings.ToLower(reason), "loop detected")
}

// isContextLimitError detects gateway context-window overflow errors so the
// agent can compact and retry the step once, mirroring Codex.
func isContextLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"context_length_exceeded",
		"context length",
		"maximum context",
		"context window",
		"too many tokens",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func previewText(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// accountUsage accumulates the token cost of one model call. The gateway
// usage chunk is authoritative when present. When it is missing (for example
// a stream canceled with Ctrl+C before the final chunk), the prompt and any
// already-streamed completion are estimated locally: the request was already
// billed even though the final accounting never arrived. It reports whether
// the call reached the model, so callers know whether to refresh the context
// footprint too.
func (a *Agent) accountUsage(ctx context.Context, sent []llm.ChatMessage, resp *llm.ChatMessage, tools []map[string]any, err error) bool {
	if resp == nil && err != nil && ctx.Err() == nil &&
		!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	if resp != nil && resp.Usage != nil && resp.Usage.TotalTokens > 0 {
		a.usage.PromptTokens += resp.Usage.PromptTokens
		a.usage.CompletionTokens += resp.Usage.CompletionTokens
		a.usage.TotalTokens += resp.Usage.TotalTokens
		a.usage.PromptCacheHitTokens += resp.Usage.CacheHitTokens()
		a.usage.PromptCacheMissTokens += resp.Usage.PromptCacheMissTokens
		return true
	}

	prompt := ccontext.CountRequestTokens(sent, tools)
	completion := 0
	if resp != nil {
		completion = ccontext.CountMessagesTokens([]llm.ChatMessage{*resp})
	}
	a.usage.PromptTokens += prompt
	a.usage.CompletionTokens += completion
	a.usage.TotalTokens += prompt + completion
	return true
}

// updateContextUsage records the context footprint after a model response.
// The gateway usage chunk (prompt + completion = the full conversation that
// the next request will replay) is authoritative; without it we estimate the
// request we just sent plus any partial response. An interrupted turn is
// therefore reflected immediately (the tokens were consumed) and is corrected
// by the next successful response, whose usage describes the replayed context.
func (a *Agent) updateContextUsage(sent []llm.ChatMessage, resp *llm.ChatMessage, tools []map[string]any) {
	if resp != nil && resp.Usage != nil && resp.Usage.TotalTokens > 0 {
		a.contextTokens = resp.Usage.TotalTokens
		return
	}
	view := make([]llm.ChatMessage, 0, len(sent)+1)
	view = append(view, sent...)
	if resp != nil {
		view = append(view, *resp)
	}
	a.contextTokens = ccontext.CountRequestTokens(view, tools)
}
