package main

import (
	"agy/internal/agent"
	"agy/internal/llm"
	"agy/internal/permission"
	"agy/internal/tools"
	"agy/internal/trajectory"
	"agy/internal/types"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type mockDoer struct {
	responses []string
	callIdx   int32
}

func (m *mockDoer) Do(req *http.Request) (*http.Response, error) {
	idx := int(atomic.AddInt32(&m.callIdx, 1) - 1)
	if idx >= len(m.responses) {
		return nil, fmt.Errorf("unexpected extra call %d", idx)
	}
	body := m.responses[idx]
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     make(http.Header),
	}, nil
}

// interruptDoer streams the supplied SSE payload and then fails with
// context.Canceled, emulating a Ctrl+C in the middle of a model response.
type interruptDoer struct{ body string }

func (m *interruptDoer) Do(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       &interruptReader{data: []byte(m.body)},
		Header:     make(http.Header),
	}, nil
}

type interruptReader struct{ data []byte }

func (r *interruptReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, context.Canceled
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func (r *interruptReader) Close() error { return nil }

func sseChunk(content string, toolCalls []map[string]any) string {
	choice := map[string]any{
		"index": 0,
		"delta": map[string]any{
			"content": content,
		},
	}
	if len(toolCalls) > 0 {
		choice["delta"].(map[string]any)["tool_calls"] = toolCalls
	}
	chunk := map[string]any{
		"choices": []any{choice},
	}
	b, _ := json.Marshal(chunk)
	return fmt.Sprintf("data: %s\n\ndata: [DONE]\n\n", string(b))
}

func TestAgentMultiStepExecution(t *testing.T) {
	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "auto.txt")

	// Model response 1: Call write_to_file
	resp1 := sseChunk("Creating the requested file...", []map[string]any{
		{
			"id":   "call_write_1",
			"type": "function",
			"function": map[string]any{
				"name": "write_to_file",
				"arguments": fmt.Sprintf(`{"TargetFile":"%s","CodeContent":"Hello ACode"}`,
					strings.ReplaceAll(targetFile, `\`, `\\`)),
			},
		},
	})

	// Model response 2: Call read_file
	resp2 := sseChunk("Checking content...", []map[string]any{
		{
			"id":   "call_read_1",
			"type": "function",
			"function": map[string]any{
				"name": "read_file",
				"arguments": fmt.Sprintf(`{"AbsolutePath":"%s"}`,
					strings.ReplaceAll(targetFile, `\`, `\\`)),
			},
		},
	})

	// Model response 3: Final answer without tool calls
	resp3 := sseChunk("Task completed! The file has been verified.", nil)

	mock := &mockDoer{
		responses: []string{resp1, resp2, resp3},
	}

	cfg := llm.Config{
		BaseURL: "http://mock-gateway",
		Model:   "mock-model",
	}
	client := llm.NewClient(cfg, mock)
	registry := tools.NewRegistry()

	var finishReason string
	testListener := &testEventListener{
		onFinish: func(reason string) {
			finishReason = reason
		},
	}

	ag := agent.NewAgent(agent.AgentConfig{
		MaxSteps: 10,
	}, client, registry, testListener)

	traj := trajectory.New("traj-integration", "cas-1")
	result, err := ag.Run(context.Background(), traj, "Please create auto.txt and verify it.")

	if err != nil {
		t.Fatalf("agent run failed: %v", err)
	}

	if result != "Task completed! The file has been verified." {
		t.Errorf("unexpected final result: %s", result)
	}

	if finishReason != "final_answer_reached" {
		t.Errorf("expected finishReason 'final_answer_reached', got '%s'", finishReason)
	}

	// Trajectory must have:
	// 1. UserInput
	// 2. PlannerResponse (write)
	// 3. ToolExecution (write)
	// 4. PlannerResponse (read)
	// 5. ToolExecution (read)
	// 6. PlannerResponse (final answer)
	if traj.NumSteps() != 6 {
		t.Fatalf("expected 6 steps in trajectory, got %d", traj.NumSteps())
	}
}

func TestStepBudgetCompactsAndContinues(t *testing.T) {
	// Codex-style behavior: reaching the step budget must compact the context
	// into a checkpoint and keep the run alive, never stop with "max steps
	// exceeded".
	respTool := sseChunk("working", []map[string]any{
		{
			"id":   "call_echo",
			"type": "function",
			"function": map[string]any{
				"name":      "run_command",
				"arguments": `{"CommandLine":"echo ok"}`,
			},
		},
	})
	respSummary := sseChunk("condensed summary", nil)
	respFinal := sseChunk("final answer", nil)

	mock := &mockDoer{responses: []string{respTool, respTool, respSummary, respFinal}}
	client := llm.NewClient(llm.Config{BaseURL: "http://mock-gateway", Model: "mock"}, mock)
	registry := tools.NewRegistry()

	var finishReason string
	testListener := &testEventListener{onFinish: func(reason string) { finishReason = reason }}

	// Step budget of 4: user(1) + planner/tool pairs reach it mid-run.
	ag := agent.NewAgent(agent.AgentConfig{MaxSteps: 4}, client, registry, testListener)

	traj := trajectory.New("", "")
	result, err := ag.Run(context.Background(), traj, "Step bounded task")
	if err != nil {
		t.Fatal(err)
	}

	if result != "final answer" {
		t.Fatalf("run must continue after the step budget compaction, got: %q", result)
	}
	if strings.Contains(finishReason, "continuation cap") {
		t.Fatalf("step budget must not stop the run: %q", finishReason)
	}
	checkpoints := 0
	for _, s := range traj.Steps() {
		if s.Header.StepType == types.StepTypeCheckpoint {
			checkpoints++
		}
	}
	if checkpoints != 1 {
		t.Fatalf("expected 1 compaction checkpoint at the step budget, got %d", checkpoints)
	}
}

func TestLoopDetectorHook(t *testing.T) {
	// Model calls identical tool call repeatedly
	respTool := sseChunk("Checking...", []map[string]any{
		{
			"id":   "call_dup",
			"type": "function",
			"function": map[string]any{
				"name":      "run_command",
				"arguments": `{"CommandLine":"git status"}`,
			},
		},
	})

	mock := &mockDoer{
		responses: []string{respTool, respTool, respTool, respTool},
	}

	cfg := llm.Config{BaseURL: "http://mock-gateway", Model: "mock"}
	client := llm.NewClient(cfg, mock)
	registry := tools.NewRegistry()

	var finishReason string
	testListener := &testEventListener{
		onFinish: func(reason string) { finishReason = reason },
	}

	ag := agent.NewAgent(agent.AgentConfig{
		MaxSteps: 20,
	}, client, registry, testListener)

	traj := trajectory.New("", "")
	_, err := ag.Run(context.Background(), traj, "Repetitive task")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(finishReason, "loop detected") {
		t.Errorf("expected loop detection trigger, got: %s", finishReason)
	}
}

type testEventListener struct {
	agent.DefaultEventListener
	onFinish func(reason string)
}

func (t *testEventListener) OnFinish(reason string) {
	if t.onFinish != nil {
		t.onFinish(reason)
	}
}

func TestApprovalErrorFeedsBackToModel(t *testing.T) {
	// A malformed tool call must not kill the whole run: the approval
	// failure is surfaced as a tool error so the model can self-correct.
	respTool := sseChunk("Running the command...", []map[string]any{
		{
			"id":   "call_bad",
			"type": "function",
			"function": map[string]any{
				"name":      "run_command",
				"arguments": `{"CommandLine":"pwd"}{`,
			},
		},
	})
	respFinal := sseChunk("Recovered with a corrected call.", nil)

	mock := &mockDoer{responses: []string{respTool, respFinal}}
	cfg := llm.Config{BaseURL: "http://mock-gateway", Model: "mock"}
	client := llm.NewClient(cfg, mock)
	registry := tools.NewRegistry()

	testListener := &approvalFailListener{}
	ag := agent.NewAgent(agent.AgentConfig{MaxSteps: 10}, client, registry, testListener)

	traj := trajectory.New("", "")
	result, err := ag.Run(context.Background(), traj, "Do something")
	if err != nil {
		t.Fatalf("run must survive an approval failure: %v", err)
	}
	if result != "Recovered with a corrected call." {
		t.Fatalf("result: %q", result)
	}
	if !strings.Contains(testListener.toolErrText, "tool approval failed") {
		t.Fatalf("approval error not fed back to the model: %q", testListener.toolErrText)
	}
}

// approvalFailListener rejects every approval with a parse error, emulating
// a corrupted tool call, and records the tool error handed to the agent.
type approvalFailListener struct {
	testEventListener
	toolErrText string
}

func (a *approvalFailListener) ApproveToolCall(call types.ToolCall) (bool, error) {
	return false, fmt.Errorf("invalid run_command arguments: invalid character '{' after top-level value")
}

func (a *approvalFailListener) OnToolResult(call types.ToolCall, result string, err error) {
	if err != nil {
		a.toolErrText = err.Error()
	}
}

func TestAgentTracksContextTokensFromUsage(t *testing.T) {
	// The usage chunk reported by the gateway is authoritative and must be
	// surfaced through ContextTokens() for the footer.
	resp := "data: {\"choices\":[{\"delta\":{\"content\":\"Hello!\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":900,\"completion_tokens\":100,\"total_tokens\":1000}}\n\n" +
		"data: [DONE]\n\n"

	mock := &mockDoer{responses: []string{resp}}
	client := llm.NewClient(llm.Config{BaseURL: "http://mock-gateway", Model: "mock"}, mock)
	registry := tools.NewRegistry()
	ag := agent.NewAgent(agent.AgentConfig{MaxSteps: 5}, client, registry, nil)

	traj := trajectory.New("", "")
	if _, err := ag.Run(context.Background(), traj, "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := ag.ContextTokens(); got != 1000 {
		t.Fatalf("context tokens: got %d, want 1000", got)
	}
}

func TestAgentEstimatesContextTokensWithoutUsage(t *testing.T) {
	// Without a usage chunk the agent falls back to a local estimate that
	// must at least account for the system prompt and the response.
	mock := &mockDoer{responses: []string{sseChunk("An answer long enough to matter.", nil)}}
	client := llm.NewClient(llm.Config{BaseURL: "http://mock-gateway", Model: "mock"}, mock)
	registry := tools.NewRegistry()
	ag := agent.NewAgent(agent.AgentConfig{MaxSteps: 5, SystemPrompt: "You are a helpful agent."}, client, registry, nil)

	traj := trajectory.New("", "")
	if _, err := ag.Run(context.Background(), traj, "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := ag.ContextTokens()
	if got < 20 {
		t.Fatalf("estimate too small: %d", got)
	}
}

func TestAgentRecordsUsageWhenStreamInterrupted(t *testing.T) {
	// Ctrl+C mid-stream means no final usage chunk. The prompt and the
	// already-streamed completion must still be accounted for, both in the
	// cumulative totals and in the context footer.
	partial := sseChunk("Partial answer that is long enough to estimate.", nil)
	// sseChunk appends [DONE]; strip it so the reader fails mid-stream.
	partial = strings.Split(partial, "data: [DONE]")[0]

	client := llm.NewClient(llm.Config{BaseURL: "http://mock-gateway", Model: "mock"},
		&interruptDoer{body: partial})
	registry := tools.NewRegistry()
	ag := agent.NewAgent(agent.AgentConfig{
		MaxSteps:     5,
		SystemPrompt: "You are a helpful agent.",
	}, client, registry, nil)

	traj := trajectory.New("", "")
	_, err := ag.Run(context.Background(), traj, "hi")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run error: got %v, want context.Canceled", err)
	}

	usage := ag.UsageTotals()
	if usage.PromptTokens < 10 {
		t.Fatalf("prompt tokens not recorded: %+v", usage)
	}
	if usage.CompletionTokens < 5 {
		t.Fatalf("interrupted completion tokens not recorded: %+v", usage)
	}
	if usage.TotalTokens != usage.PromptTokens+usage.CompletionTokens {
		t.Fatalf("inconsistent totals: %+v", usage)
	}
	if got := ag.ContextTokens(); got != usage.TotalTokens {
		t.Fatalf("context tokens after interrupt: got %d, want consumed total %d", got, usage.TotalTokens)
	}
}

func TestAgentRecordsGatewayUsage(t *testing.T) {
	resp := "data: {\"choices\":[{\"delta\":{\"content\":\"Hello!\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":900,\"completion_tokens\":100,\"total_tokens\":1000}}\n\n" +
		"data: [DONE]\n\n"

	mock := &mockDoer{responses: []string{resp}}
	client := llm.NewClient(llm.Config{BaseURL: "http://mock-gateway", Model: "mock"}, mock)
	ag := agent.NewAgent(agent.AgentConfig{MaxSteps: 5}, client, tools.NewRegistry(), nil)

	traj := trajectory.New("", "")
	if _, err := ag.Run(context.Background(), traj, "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}
	usage := ag.UsageTotals()
	if usage.PromptTokens != 900 || usage.CompletionTokens != 100 || usage.TotalTokens != 1000 {
		t.Fatalf("gateway usage must be cumulative and exact: %+v", usage)
	}
}

// captureDoer is a mock that records request bodies so tests can inspect the
// exact chat payload the agent sent.
type captureDoer struct {
	mu        sync.Mutex
	responses []string
	callIdx   int32
	bodies    []string
}

func (c *captureDoer) Do(req *http.Request) (*http.Response, error) {
	idx := int(atomic.AddInt32(&c.callIdx, 1) - 1)
	if idx >= len(c.responses) {
		return nil, fmt.Errorf("unexpected extra call %d", idx)
	}
	body, _ := io.ReadAll(req.Body)
	c.mu.Lock()
	c.bodies = append(c.bodies, string(body))
	c.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewBufferString(c.responses[idx])),
		Header:     make(http.Header),
	}, nil
}

func (c *captureDoer) requestBodies() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.bodies))
	copy(out, c.bodies)
	return out
}

type compactionListener struct {
	testEventListener
	onChange func(before, after int)
}

func (l *compactionListener) OnCompaction(before, after int) {
	if l.onChange != nil {
		l.onChange(before, after)
	}
}

func TestSystemPromptIncludesProgressProtocol(t *testing.T) {
	mock := &captureDoer{responses: []string{sseChunk("ok", nil)}}
	client := llm.NewClient(llm.Config{BaseURL: "http://mock-gateway", Model: "mock"}, mock)
	registry := tools.NewRegistry()
	ag := agent.NewAgent(agent.AgentConfig{MaxSteps: 5, SystemPrompt: "Base prompt."}, client, registry, nil)

	traj := trajectory.New("", "")
	if _, err := ag.Run(context.Background(), traj, "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}
	bodies := mock.requestBodies()
	if len(bodies) == 0 {
		t.Fatalf("no request captured")
	}
	first := bodies[0]
	if !strings.Contains(first, "Progress protocol") {
		t.Fatalf("progress protocol missing from system prompt: %s", first)
	}
	if !strings.Contains(first, "进度") || !strings.Contains(first, "完成度") {
		t.Fatalf("progress/completion wording missing from system prompt: %s", first)
	}
}

func TestCheckpointReplayUsesSummary(t *testing.T) {
	mock := &captureDoer{responses: []string{sseChunk("ok", nil)}}
	client := llm.NewClient(llm.Config{BaseURL: "http://mock-gateway", Model: "mock"}, mock)
	registry := tools.NewRegistry()
	ag := agent.NewAgent(agent.AgentConfig{MaxSteps: 5, SystemPrompt: "Base prompt."}, client, registry, nil)

	traj := trajectory.New("", "")
	traj.AppendStep(trajectory.NewUserInputStep("first question"))
	traj.AppendStep(trajectory.NewPlannerResponseStep("old answer", nil))
	traj.AppendStep(trajectory.NewToolExecutionStep("run_command", "call_1", "SECRET-OLD-OUTPUT", nil))
	traj.AppendStep(trajectory.NewCheckpointStep("handoff text", []string{"first question"}, 99999, 42))

	if _, err := ag.Run(context.Background(), traj, "second question"); err != nil {
		t.Fatalf("run: %v", err)
	}
	body := mock.requestBodies()[0]
	if !strings.Contains(body, "handoff text") {
		t.Fatalf("checkpoint summary missing from replay: %s", body)
	}
	if strings.Contains(body, "SECRET-OLD-OUTPUT") {
		t.Fatalf("pre-checkpoint tool output leaked into replay: %s", body)
	}
	if !strings.Contains(body, "second question") {
		t.Fatalf("post-checkpoint user input missing from replay: %s", body)
	}
}

func TestAutoCompactionCreatesCheckpoint(t *testing.T) {
	bigArgs := fmt.Sprintf(`{"CommandLine":"echo %s"}`, strings.Repeat("x", 3000))
	respTool := sseChunk("running", []map[string]any{
		{
			"id":   "call_big",
			"type": "function",
			"function": map[string]any{
				"name":      "run_command",
				"arguments": bigArgs,
			},
		},
	})
	respSummary := sseChunk("condensed summary", nil)
	respFinal := sseChunk("all done", nil)

	mock := &mockDoer{responses: []string{respTool, respSummary, respFinal}}
	client := llm.NewClient(llm.Config{BaseURL: "http://mock-gateway", Model: "mock"}, mock)
	registry := tools.NewRegistry()

	var changed bool
	testListener := &compactionListener{onChange: func(before, after int) {
		changed = true
		if before <= after {
			t.Errorf("compaction must shrink context: before=%d after=%d", before, after)
		}
	}}

	ag := agent.NewAgent(agent.AgentConfig{
		MaxSteps:              10,
		SystemPrompt:          "sys",
		AutoCompactTokenLimit: 700,
	}, client, registry, testListener)

	traj := trajectory.New("", "")
	result, err := ag.Run(context.Background(), traj, "go")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result != "all done" {
		t.Fatalf("result: %q", result)
	}
	if !changed {
		t.Fatalf("OnCompaction was never fired")
	}
	checkpoints := 0
	for _, s := range traj.Steps() {
		if s.Header.StepType == types.StepTypeCheckpoint {
			checkpoints++
		}
	}
	if checkpoints != 1 {
		t.Fatalf("expected 1 checkpoint step, got %d", checkpoints)
	}
}

func TestLoopDetectionWrapUp(t *testing.T) {
	respTool := sseChunk("still working", []map[string]any{
		{
			"id":   "call_echo",
			"type": "function",
			"function": map[string]any{
				"name":      "run_command",
				"arguments": `{"CommandLine":"echo ok"}`,
			},
		},
	})
	respWrap := sseChunk("Work stopped after a loop. 完成度: 40% · 未完成: 剩余调查", nil)

	mock := &mockDoer{responses: []string{respTool, respTool, respTool, respWrap}}
	client := llm.NewClient(llm.Config{BaseURL: "http://mock-gateway", Model: "mock"}, mock)
	registry := tools.NewRegistry()

	var finishReason string
	testListener := &testEventListener{onFinish: func(reason string) { finishReason = reason }}

	ag := agent.NewAgent(agent.AgentConfig{MaxSteps: 50}, client, registry, testListener)

	traj := trajectory.New("", "")
	result, err := ag.Run(context.Background(), traj, "Looping task")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(result, "完成度") {
		t.Fatalf("wrap-up answer missing completion degree: %q", result)
	}
	if !strings.Contains(finishReason, "loop detected") {
		t.Fatalf("finish reason: %q", finishReason)
	}
}

func TestPermissionGateFeedsToolError(t *testing.T) {
	respWrite := sseChunk("writing", []map[string]any{
		{
			"id":   "call_perm",
			"type": "function",
			"function": map[string]any{
				"name":      "write_to_file",
				"arguments": `{"TargetFile":"/etc/forbidden_agy.txt","CodeContent":"x"}`,
			},
		},
	})
	respFinal := sseChunk("Stopped. 完成度: 10%", nil)

	mock := &captureDoer{responses: []string{respWrite, respFinal}}
	client := llm.NewClient(llm.Config{BaseURL: "http://mock-gateway", Model: "mock"}, mock)
	registry := tools.NewRegistry()

	gate, err := permission.NewGate(permission.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	ag := agent.NewAgent(agent.AgentConfig{MaxSteps: 10, Permission: gate}, client, registry, nil)
	traj := trajectory.New("", "")
	if _, err := ag.Run(context.Background(), traj, "try writing"); err != nil {
		t.Fatalf("run: %v", err)
	}

	// The second request must carry the gate denial as the tool result.
	if len(mock.bodies) < 2 {
		t.Fatalf("expected 2 model calls, got %d", len(mock.bodies))
	}
	if !strings.Contains(mock.bodies[1], "permission denied") {
		t.Fatalf("denial not fed back to model: %s", mock.bodies[1])
	}
	// And the forbidden file must not exist.
	if _, statErr := os.Stat("/etc/forbidden_agy.txt"); !os.IsNotExist(statErr) {
		t.Fatalf("readonly mode wrote the file anyway")
	}
}
