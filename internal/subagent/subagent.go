package subagent

import (
	"agy/internal/agent"
	"agy/internal/llm"
	"agy/internal/permission"
	"agy/internal/tools"
	"agy/internal/trajectory"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type SubagentSpec struct {
	Role     string `json:"Role"`
	Prompt   string `json:"Prompt"`
	TypeName string `json:"TypeName,omitempty"`
	Model    string `json:"Model,omitempty"`
	Fork     bool   `json:"Fork,omitempty"`
}

type SubagentInstance struct {
	ID        string
	Role      string
	Prompt    string
	StartTime time.Time
	Status    string
	Result    string
	Traj      *trajectory.Trajectory
}

type Manager struct {
	mu        sync.RWMutex
	instances map[string]*SubagentInstance
	llmClient *llm.Client
	registry  *tools.Registry

	// BaseSystemPrompt is the immutable root system prompt shared with the
	// parent agent so child agents match the parent's prefix cache block.
	BaseSystemPrompt string

	// MaxStepsPerSubagent bounds each child run's continuation budget
	// (Codex-style scope box). Zero means the default of 30.
	MaxStepsPerSubagent int
	// AutoCompactTokenLimit is propagated to child agents so a long
	// subagent also compacts instead of overflowing the context window.
	AutoCompactTokenLimit int
	// Permission is the sandbox gate inherited by every subagent so scoped
	// children cannot evade the session's permission mode.
	Permission *permission.Gate
}

func NewManager(client *llm.Client, reg *tools.Registry) *Manager {
	return &Manager{
		instances:           make(map[string]*SubagentInstance),
		llmClient:           client,
		registry:            reg,
		MaxStepsPerSubagent: 200,
	}
}

func (m *Manager) Spawn(ctx context.Context, spec SubagentSpec, parentTraj *trajectory.Trajectory) (*SubagentInstance, error) {
	m.mu.Lock()
	id := fmt.Sprintf("subagent-%d", time.Now().UnixNano()%1000000)
	parentCascade := ""
	if parentTraj != nil {
		parentCascade = parentTraj.CascadeID()
	}
	inst := &SubagentInstance{
		ID:        id,
		Role:      spec.Role,
		Prompt:    spec.Prompt,
		StartTime: time.Now(),
		Status:    "RUNNING",
		Traj:      trajectory.New(id, parentCascade),
	}
	m.instances[id] = inst
	m.mu.Unlock()

	// If Fork is requested, replay parent trajectory into child
	if spec.Fork && parentTraj != nil {
		for _, s := range parentTraj.Steps() {
			inst.Traj.AppendStep(s)
		}
	}

	maxSteps := m.MaxStepsPerSubagent
	if maxSteps <= 0 {
		maxSteps = 30
	}

	// Preserve the root system prompt prefix so both parent and child agents
	// share the exact same system prompt block and KV cache. The subagent's
	// specialized role and bounded scope are passed in the prompt turn
	// (mirroring DeepSeek-Harness's in-history update and Codex subagents),
	// preventing a 100% cache miss on message[0].
	baseSysPrompt := m.BaseSystemPrompt
	if baseSysPrompt == "" {
		baseSysPrompt = agent.DefaultSystemPrompt
	}

	childConfig := agent.AgentConfig{
		SystemPrompt:          baseSysPrompt,
		MaxSteps:              maxSteps,
		AutoCompactTokenLimit: m.AutoCompactTokenLimit,
		Permission:            m.Permission,
	}
	childAgent := agent.NewAgent(childConfig, m.llmClient, m.registry, nil)

	scopeDirective := fmt.Sprintf("[Subagent Delegation: %s]\nTask: %s\nStay strictly within this scope, do not ask follow-up questions, and prefer concise findings over long prose.",
		spec.Role, spec.Prompt)

	res, err := childAgent.Run(ctx, inst.Traj, scopeDirective)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		inst.Status = "ERROR: " + err.Error()
		inst.Result = "Error: " + err.Error()
		return inst, err
	}
	inst.Status = "DONE"
	inst.Result = res
	return inst, nil
}

func (m *Manager) List() []*SubagentInstance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*SubagentInstance, 0, len(m.instances))
	for _, inst := range m.instances {
		out = append(out, inst)
	}
	return out
}

func (m *Manager) Kill(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok := m.instances[id]; ok {
		inst.Status = "CANCELLED"
		return true
	}
	return false
}

// ----------------------------------------------------------------------
// InvokeSubagentTool
// ----------------------------------------------------------------------

type InvokeSubagentTool struct {
	mgr *Manager
}

func NewInvokeSubagentTool(mgr *Manager) *InvokeSubagentTool {
	return &InvokeSubagentTool{mgr: mgr}
}

func (t *InvokeSubagentTool) Name() string { return "invoke_subagent" }
func (t *InvokeSubagentTool) Description() string {
	return "Invokes one or more subagents concurrently to handle subtasks in their own isolated context."
}
func (t *InvokeSubagentTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"Subagents": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"Role":     map[string]any{"type": "string", "description": "The subagent's role title"},
						"Prompt":   map[string]any{"type": "string", "description": "Actionable task description"},
						"TypeName": map[string]any{"type": "string", "description": "Type name (e.g. researcher, coder)"},
						"Model":    map[string]any{"type": "string", "description": "inherit, flash, or pro"},
						"Fork":     map[string]any{"type": "boolean", "description": "Whether to fork parent history"},
					},
					"required": []string{"Role", "Prompt"},
				},
			},
		},
		"required": []string{"Subagents"},
	}
}

type InvokeSubagentArgs struct {
	Subagents []SubagentSpec `json:"Subagents"`
}

func (t *InvokeSubagentTool) Execute(ctx context.Context, argsJSON []byte) (string, error) {
	var args InvokeSubagentArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return "", fmt.Errorf("invoke_subagent: failed to unmarshal args: %w", err)
	}
	if len(args.Subagents) == 0 {
		return "", fmt.Errorf("invoke_subagent: Subagents array must not be empty")
	}

	var results []string
	for i, spec := range args.Subagents {
		inst, err := t.mgr.Spawn(ctx, spec, nil)
		if err != nil {
			results = append(results, fmt.Sprintf("[Subagent %d (%s) failed]: %v", i+1, spec.Role, err))
		} else {
			results = append(results, fmt.Sprintf("[Subagent %s (%s) finished]:\n%s", inst.ID, spec.Role, inst.Result))
		}
	}

	var out string
	for _, r := range results {
		out += r + "\n\n"
	}
	return out, nil
}
