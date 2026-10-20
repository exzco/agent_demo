package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestDefaultKeybindings(t *testing.T) {
	data, err := os.ReadFile("../../notes/keybindings_default.json")
	if err != nil {
		t.Skipf("keybindings json not found: %v", err)
	}

	var bindings map[string]string
	if err := json.Unmarshal(data, &bindings); err != nil {
		t.Fatalf("failed to parse keybindings json: %v", err)
	}

	if len(bindings) != 77 {
		t.Errorf("expected 77 keybindings, got %d", len(bindings))
	}

	// Verify essential keybindings
	checks := map[string]string{
		"KeyExit":             "Exit",
		"KeyEnter":            "Select",
		"KeyEscape":           "Cancel",
		"KeyToggleTrajectory": "Toggle Trajectory",
		"KeyOpenEditor":       "Open Editor",
		"KeyApprove":          "Approve",
		"KeyReject":           "Reject",
		"KeyReviewArtifact":   "Review",
	}

	for k, want := range checks {
		if got, ok := bindings[k]; !ok || got != want {
			t.Errorf("keybinding %s mismatch: got %q, want %q", k, got, want)
		}
	}
}
