package main

import (
	"fmt"
	"testing"
	"sort"
)

func TestMapRandomOrder(t *testing.T) {
	tools := map[string]string{
		"read":       "read a file",
		"write":      "write a file",
		"edit":       "edit a file",
		"run":        "run a command",
		"glob":       "find files by pattern",
		"grep":       "search file contents",
		"ls":         "list directory",
		"diff":       "show diff",
		"patch":      "apply patch",
		"spill_view": "view spilled output",
	}

	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)

	for round := 1; round <= 5; round++ {
		fmt.Printf("[round %d] ", round)
		for _, name := range names {
			fmt.Printf("%s ", name)
		}
		fmt.Println()
	}
}
