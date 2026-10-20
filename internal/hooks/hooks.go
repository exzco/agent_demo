package hooks

import (
	"agy/internal/trajectory"
	"agy/internal/types"
	"fmt"
)

type Hook interface {
	Name() string
	OnStep(traj *trajectory.Trajectory, step trajectory.Step) (stop bool, reason string, err error)
}

// ----------------------------------------------------------------------
// NoToolCallHook (from posthooks/no_tool_call.go)
// ----------------------------------------------------------------------
type NoToolCallHook struct{}

func (h *NoToolCallHook) Name() string { return "no_tool_call_check" }

func (h *NoToolCallHook) OnStep(traj *trajectory.Trajectory, step trajectory.Step) (bool, string, error) {
	if step.Header.StepType == types.StepTypePlannerResponse {
		if !step.Header.Metadata.HasToolCall {
			return true, "final_answer_reached", nil
		}
	}
	return false, "", nil
}

// ----------------------------------------------------------------------
// LoopDetectionHook (from utils/loop_detection.go)
// ----------------------------------------------------------------------
type LoopDetectionHook struct {
	MaxConsecutiveSameCalls int
}

func (h *LoopDetectionHook) Name() string { return "loop_detection" }

func (h *LoopDetectionHook) OnStep(traj *trajectory.Trajectory, step trajectory.Step) (bool, string, error) {
	if h.MaxConsecutiveSameCalls <= 0 {
		h.MaxConsecutiveSameCalls = 3
	}

	steps := traj.Steps()
	var toolSteps []trajectory.Step
	for _, s := range steps {
		if s.Header.StepType == types.StepTypeToolExecution {
			toolSteps = append(toolSteps, s)
		}
	}

	if len(toolSteps) < h.MaxConsecutiveSameCalls {
		return false, "", nil
	}

	n := h.MaxConsecutiveSameCalls
	lastSteps := toolSteps[len(toolSteps)-n:]
	allSame := true
	var firstName, firstArgs string

	for i, s := range lastSteps {
		name := s.Header.Metadata.ToolCallName
		args := s.Header.Metadata.ToolCallArguments
		if i == 0 {
			firstName = name
			firstArgs = args
		} else {
			if name != firstName || args != firstArgs {
				allSame = false
				break
			}
		}
	}

	if allSame && firstName != "" {
		return true, fmt.Sprintf("loop detected: repeated tool call %s identical %d times", firstName, n), nil
	}

	return false, "", nil
}

// ----------------------------------------------------------------------
// HookRunner
// ----------------------------------------------------------------------
type HookRunner struct {
	hooks []Hook
}

// NewHookRunner builds the stop-hook pipeline. The step budget is not a hook:
// reaching it triggers a compaction checkpoint in the agent loop (Codex-style
// context management) rather than stopping the run.
func NewHookRunner(maxSteps int) *HookRunner {
	return &HookRunner{
		hooks: []Hook{
			&NoToolCallHook{},
			&LoopDetectionHook{MaxConsecutiveSameCalls: 3},
		},
	}
}

func (r *HookRunner) Evaluate(traj *trajectory.Trajectory, step trajectory.Step) (stop bool, reason string, err error) {
	for _, h := range r.hooks {
		stop, reason, err := h.OnStep(traj, step)
		if err != nil {
			return true, "", fmt.Errorf("hook %s failed: %w", h.Name(), err)
		}
		if stop {
			return true, reason, nil
		}
	}
	return false, "", nil
}
