package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportOpenAIToolsDeterministicOrder(t *testing.T) {
	r := NewRegistry()

	var firstOrder []string
	firstTools := r.ExportOpenAITools()
	for _, tool := range firstTools {
		fn := tool["function"].(map[string]any)
		firstOrder = append(firstOrder, fn["name"].(string))
	}

	// Verify that the tools are in alphabetical order
	for i := 1; i < len(firstOrder); i++ {
		if firstOrder[i-1] > firstOrder[i] {
			t.Fatalf("tools not sorted alphabetically: %v", firstOrder)
		}
	}

	// Verify deterministic ordering over 100 iterations
	for round := 0; round < 100; round++ {
		tools := r.ExportOpenAITools()
		if len(tools) != len(firstOrder) {
			t.Fatalf("length mismatch at round %d: got %d, want %d", round, len(tools), len(firstOrder))
		}
		for i, tool := range tools {
			fn := tool["function"].(map[string]any)
			name := fn["name"].(string)
			if name != firstOrder[i] {
				t.Fatalf("ordering mismatch at round %d, index %d: got %s, want %s", round, i, name, firstOrder[i])
			}
		}
	}
}

func TestTruncateToolOutput(t *testing.T) {
	shortOutput := "This is a short output that fits comfortably within limits."
	res := TruncateToolOutput(shortOutput, 100)
	if res != shortOutput {
		t.Fatalf("expected short output to be unchanged, got: %s", res)
	}

	// Create output exceeding 1000 characters
	var sb strings.Builder
	for i := 1; i <= 200; i++ {
		sb.WriteString(fmt.Sprintf("Line %03d: Some detailed command output data\n", i))
	}
	largeOutput := sb.String()

	truncated := TruncateToolOutput(largeOutput, 200)
	if len(truncated) >= len(largeOutput) {
		t.Fatalf("expected truncated output to be smaller than original: got %d, orig %d", len(truncated), len(largeOutput))
	}
	if !strings.Contains(truncated, "Omitted") || !strings.Contains(truncated, "Truncated to protect token budget") {
		t.Fatalf("expected truncation notice in output: %s", truncated)
	}
	if !strings.HasPrefix(truncated, "Line 001:") {
		t.Fatalf("expected head preservation: %s", truncated[:50])
	}
	if !strings.Contains(truncated, "Line 200:") {
		t.Fatalf("expected tail preservation")
	}
}

func TestReadFileWithTruncation(t *testing.T) {
	tmpDir := t.TempDir()
	largeFile := filepath.Join(tmpDir, "large.txt")

	var sb strings.Builder
	for i := 1; i <= 1000; i++ {
		sb.WriteString(fmt.Sprintf("Line %d: data payload\n", i))
	}
	if err := os.WriteFile(largeFile, []byte(sb.String()), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadFileTool{}
	args := fmt.Sprintf(`{"AbsolutePath": %q}`, largeFile)
	out, err := tool.Execute(context.Background(), []byte(args))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Omitted") {
		t.Fatalf("expected 1000 lines file read to be truncated by retention policy: len=%d", len(out))
	}
}

func TestTruncateToolOutputDeterministicSpill(t *testing.T) {
	payload := strings.Repeat("A long repetitive output line to exceed maxChars threshold.\n", 100)
	res1 := TruncateToolOutput(payload, 200)
	res2 := TruncateToolOutput(payload, 200)

	if res1 != res2 {
		t.Fatalf("TruncateToolOutput is non-deterministic!\nGot 1: %s\nGot 2: %s", res1, res2)
	}

	// Verify that the spill file contains a hash-based name rather than variable timestamp
	if !strings.Contains(res1, "agy-tool-output-") {
		t.Fatalf("expected agy-tool-output- in output: %s", res1)
	}
}

