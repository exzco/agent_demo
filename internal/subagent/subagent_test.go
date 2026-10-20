package subagent

import (
	"agy/internal/llm"
	"agy/internal/tools"
	"agy/internal/trajectory"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type mockSubagentDoer struct {
	responses []string
	requests  []string
	callIdx   int32
}

func (m *mockSubagentDoer) Do(req *http.Request) (*http.Response, error) {
	bodyBytes, _ := io.ReadAll(req.Body)
	m.requests = append(m.requests, string(bodyBytes))

	idx := int(atomic.AddInt32(&m.callIdx, 1) - 1)
	if idx >= len(m.responses) {
		return nil, fmt.Errorf("unexpected call %d", idx)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewBufferString(m.responses[idx])),
		Header:     make(http.Header),
	}, nil
}

func sseMessage(text string) string {
	chunk := map[string]any{
		"choices": []any{
			map[string]any{
				"index": 0,
				"delta": map[string]any{
					"content": text,
				},
			},
		},
	}
	b, _ := json.Marshal(chunk)
	return fmt.Sprintf("data: %s\n\ndata: [DONE]\n\n", string(b))
}

func TestSubagentPreservesParentPrefix(t *testing.T) {
	mock := &mockSubagentDoer{
		responses: []string{sseMessage("subagent result")},
	}
	client := llm.NewClient(llm.Config{BaseURL: "http://mock-gw", Model: "mock"}, mock)
	reg := tools.NewRegistry()

	basePrompt := "You are ACode, a powerful AI coding agent. Fixed prefix."
	mgr := NewManager(client, reg)
	mgr.BaseSystemPrompt = basePrompt

	// Create parent trajectory with 2 steps
	parentTraj := trajectory.New("parent-casc", "")
	parentTraj.AppendStep(trajectory.NewUserInputStep("Parent initial prompt"))
	parentTraj.AppendStep(trajectory.NewPlannerResponseStep("Parent thought and plan", nil))

	inst, err := mgr.Spawn(context.Background(), SubagentSpec{
		Role:   "Reviewer",
		Prompt: "Check security boundaries",
		Fork:   true,
	}, parentTraj)

	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}
	if inst.Status != "DONE" {
		t.Fatalf("subagent status not done: %s", inst.Status)
	}

	if len(mock.requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(mock.requests))
	}

	reqBody := mock.requests[0]
	// Verify that the system prompt sent in messages[0] is the base system prompt
	if !strings.Contains(reqBody, "Fixed prefix.") {
		t.Fatalf("subagent did not preserve BaseSystemPrompt in request: %s", reqBody)
	}
	// Verify parent steps are included
	if !strings.Contains(reqBody, "Parent initial prompt") || !strings.Contains(reqBody, "Parent thought and plan") {
		t.Fatalf("forked subagent missing parent steps: %s", reqBody)
	}
	// Verify subagent role is in the delegation prompt, NOT overwriting system prompt
	if !strings.Contains(reqBody, "[Subagent Delegation: Reviewer]") {
		t.Fatalf("subagent missing role delegation directive: %s", reqBody)
	}
}
