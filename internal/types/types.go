package types

import "time"

type StepType int

const (
	StepTypeUnspecified      StepType = 0
	StepTypeUserInput        StepType = 1
	StepTypePlannerResponse  StepType = 2
	StepTypeToolExecution    StepType = 3
	StepTypeEphemeralMessage StepType = 4
	StepTypeCheckpoint       StepType = 5
)

func (s StepType) String() string {
	switch s {
	case StepTypeUserInput:
		return "USER_INPUT"
	case StepTypePlannerResponse:
		return "PLANNER_RESPONSE"
	case StepTypeToolExecution:
		return "TOOL_EXECUTION"
	case StepTypeEphemeralMessage:
		return "EPHEMERAL_MESSAGE"
	case StepTypeCheckpoint:
		return "CHECKPOINT"
	default:
		return "UNSPECIFIED"
	}
}

type StepStatus int

const (
	StepStatusUnspecified StepStatus = 0
	StepStatusPending     StepStatus = 1
	StepStatusRunning     StepStatus = 2
	StepStatusDone        StepStatus = 3
	StepStatusError       StepStatus = 4
	StepStatusCancelled   StepStatus = 5
)

func (s StepStatus) String() string {
	switch s {
	case StepStatusPending:
		return "PENDING"
	case StepStatusRunning:
		return "RUNNING"
	case StepStatusDone:
		return "DONE"
	case StepStatusError:
		return "ERROR"
	case StepStatusCancelled:
		return "CANCELLED"
	default:
		return "UNSPECIFIED"
	}
}

type StepSource int

const (
	StepSourceUnspecified StepSource = 0
	StepSourceUser        StepSource = 1
	StepSourcePlanner     StepSource = 2
	StepSourceExecutor    StepSource = 3
	StepSourceSubagent    StepSource = 4
)

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCall struct {
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

type StepHeaderMetadata struct {
	ModelAPIContentID string
	Source            StepSource
	HasToolCall       bool
	ToolCallName      string
	ToolCallID        string
	ToolCallArguments string
	ExecutionID       string
	CreatedAt         time.Time
	FinishedAt        time.Time
	CompletedAt       time.Time
}

type StepHeader struct {
	StepType StepType
	Status   StepStatus
	Metadata StepHeaderMetadata
	ErrorMsg string
}

type StepView interface {
	Type() StepType
	Header() StepHeader
	Payload() any
}
