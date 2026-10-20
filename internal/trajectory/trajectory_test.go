package trajectory

import "testing"

func TestPlannerResponseStepReasoning(t *testing.T) {
	step := NewPlannerResponseStepWithReasoning("answer", "chains of thought", nil)
	p, ok := step.Payload.(PlannerResponsePayload)
	if !ok {
		t.Fatalf("payload type: %T", step.Payload)
	}
	if p.Text != "answer" || p.ReasoningContent != "chains of thought" {
		t.Fatalf("payload: %+v", p)
	}

	legacy := NewPlannerResponseStep("answer", nil).Payload.(PlannerResponsePayload)
	if legacy.ReasoningContent != "" {
		t.Fatalf("legacy constructor must leave reasoning empty: %+v", legacy)
	}
}
