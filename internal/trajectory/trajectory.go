package trajectory

import (
	"agy/internal/types"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type Step struct {
	Header  types.StepHeader `json:"header"`
	Payload any              `json:"payload"`
}

type UserInputPayload struct {
	Text string `json:"text"`
}

type PlannerResponsePayload struct {
	Text             string           `json:"text"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []types.ToolCall `json:"tool_calls,omitempty"`
}

type ToolExecutionPayload struct {
	ToolName string `json:"tool_name"`
	CallID   string `json:"call_id"`
	Result   string `json:"result"`
	Error    string `json:"error,omitempty"`
}

// CheckpointPayload records a compaction checkpoint: everything before it is
// replaced by the summary plus a bounded set of recent user messages, so
// trajectory replay after this point only pays for the compacted view.
type CheckpointPayload struct {
	Summary          string   `json:"summary"`
	KeptUserMessages []string `json:"kept_user_messages,omitempty"`
	BeforeTokens     int      `json:"before_tokens"`
	AfterTokens      int      `json:"after_tokens"`
}

type Trajectory struct {
	mu           sync.RWMutex
	trajectoryID string
	cascadeID    string
	steps        []Step
	createdAt    time.Time
}

func New(trajectoryID, cascadeID string) *Trajectory {
	if trajectoryID == "" {
		trajectoryID = fmt.Sprintf("traj-%d", time.Now().UnixNano())
	}
	if cascadeID == "" {
		cascadeID = fmt.Sprintf("cas-%d", time.Now().UnixNano())
	}
	return &Trajectory{
		trajectoryID: trajectoryID,
		cascadeID:    cascadeID,
		steps:        make([]Step, 0),
		createdAt:    time.Now(),
	}
}

func (t *Trajectory) TrajectoryID() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.trajectoryID
}

func (t *Trajectory) CascadeID() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.cascadeID
}

func (t *Trajectory) NumSteps() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.steps)
}

// NumCountableStepsSinceLastCheckpoint counts budget-consuming steps after the
// most recent checkpoint. Compaction checkpoints reset this counter, so the
// step budget describes one work segment rather than the whole session.
func (t *Trajectory) NumCountableStepsSinceLastCheckpoint() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	n := 0
	for i := len(t.steps) - 1; i >= 0; i-- {
		if t.steps[i].Header.StepType == types.StepTypeCheckpoint {
			break
		}
		switch t.steps[i].Header.StepType {
		case types.StepTypeEphemeralMessage:
			continue
		}
		n++
	}
	return n
}

func (t *Trajectory) Steps() []Step {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]Step, len(t.steps))
	copy(out, t.steps)
	return out
}

func (t *Trajectory) AppendStep(step Step) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.steps = append(t.steps, step)
	return len(t.steps) - 1
}

func (t *Trajectory) StepHeader(idx int) (types.StepHeader, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if idx < 0 || idx >= len(t.steps) {
		return types.StepHeader{}, false
	}
	return t.steps[idx].Header, true
}

// Helper step constructors
func NewUserInputStep(text string) Step {
	now := time.Now()
	return Step{
		Header: types.StepHeader{
			StepType: types.StepTypeUserInput,
			Status:   types.StepStatusDone,
			Metadata: types.StepHeaderMetadata{
				Source:      types.StepSourceUser,
				CreatedAt:   now,
				CompletedAt: now,
			},
		},
		Payload: UserInputPayload{Text: text},
	}
}

func NewPlannerResponseStep(text string, toolCalls []types.ToolCall) Step {
	return NewPlannerResponseStepWithReasoning(text, "", toolCalls)
}

// NewPlannerResponseStepWithReasoning records the planner reply together with
// the streamed reasoning_content, which thinking-mode gateways require to be
// sent back verbatim on subsequent turns.
func NewPlannerResponseStepWithReasoning(text, reasoning string, toolCalls []types.ToolCall) Step {
	now := time.Now()
	hasTool := len(toolCalls) > 0
	var toolName, toolID string
	if hasTool {
		toolName = toolCalls[0].Function.Name
		toolID = toolCalls[0].ID
	}
	return Step{
		Header: types.StepHeader{
			StepType: types.StepTypePlannerResponse,
			Status:   types.StepStatusDone,
			Metadata: types.StepHeaderMetadata{
				Source:       types.StepSourcePlanner,
				HasToolCall:  hasTool,
				ToolCallName: toolName,
				ToolCallID:   toolID,
				CreatedAt:    now,
				CompletedAt:  now,
			},
		},
		Payload: PlannerResponsePayload{
			Text:             text,
			ReasoningContent: reasoning,
			ToolCalls:        toolCalls,
		},
	}
}

func NewToolExecutionStep(name, callID, result string, err error) Step {
	now := time.Now()
	status := types.StepStatusDone
	var errStr string
	if err != nil {
		status = types.StepStatusError
		errStr = err.Error()
	}
	return Step{
		Header: types.StepHeader{
			StepType: types.StepTypeToolExecution,
			Status:   status,
			ErrorMsg: errStr,
			Metadata: types.StepHeaderMetadata{
				Source:       types.StepSourceExecutor,
				ToolCallName: name,
				ToolCallID:   callID,
				CreatedAt:    now,
				CompletedAt:  now,
			},
		},
		Payload: ToolExecutionPayload{
			ToolName: name,
			CallID:   callID,
			Result:   result,
			Error:    errStr,
		},
	}
}

// NewCheckpointStep records a context-compaction checkpoint in the trajectory.
// Replay resets the accumulated messages to the summary (plus the kept user
// messages) at this point, mirroring Codex's compacted-history bridge.
func NewCheckpointStep(summary string, keptUserMessages []string, beforeTokens, afterTokens int) Step {
	now := time.Now()
	return Step{
		Header: types.StepHeader{
			StepType: types.StepTypeCheckpoint,
			Status:   types.StepStatusDone,
			Metadata: types.StepHeaderMetadata{
				Source:      types.StepSourcePlanner,
				CreatedAt:   now,
				CompletedAt: now,
			},
		},
		Payload: CheckpointPayload{
			Summary:          summary,
			KeptUserMessages: keptUserMessages,
			BeforeTokens:     beforeTokens,
			AfterTokens:      afterTokens,
		},
	}
}

// JSON Serialization
type SerializedTrajectory struct {
	TrajectoryID string `json:"trajectory_id"`
	CascadeID    string `json:"cascade_id"`
	Steps        []Step `json:"steps"`
}

func (t *Trajectory) MarshalJSON() ([]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return json.Marshal(SerializedTrajectory{
		TrajectoryID: t.trajectoryID,
		CascadeID:    t.cascadeID,
		Steps:        t.steps,
	})
}

func Unmarshal(data []byte) (*Trajectory, error) {
	var s SerializedTrajectory
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	t := New(s.TrajectoryID, s.CascadeID)
	t.steps = s.Steps
	return t, nil
}
