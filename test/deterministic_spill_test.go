package main

import (
	"agy/internal/tools"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func legacyTruncateWithTimestamp(output string, maxChars int) string {
	head := output[:maxChars/2]
	tail := output[len(output)-maxChars/2:]
	spillFile := filepath.Join(os.TempDir(), fmt.Sprintf("agy-tool-output-%d.log", time.Now().UnixNano()))
	return fmt.Sprintf("%s\n[... Full output stored at: %s ...]\n%s", head, spillFile, tail)
}

func TestDeterministicSpillVsLegacy(t *testing.T) {
	output := strings.Repeat("2026-10-19 [INFO] build target success and test passed\n", 50)

	// 两次调用因时间戳不同而产生差异，破坏前缀缓存
	legacy1 := legacyTruncateWithTimestamp(output, 100)
	println(legacy1)
	time.Sleep(2 * time.Millisecond)
	legacy2 := legacyTruncateWithTimestamp(output, 100)
	println(legacy2)

	new1 := tools.TruncateToolOutput(output, 100)
	println(new1)
	time.Sleep(2 * time.Millisecond)
	new2 := tools.TruncateToolOutput(output, 100)
	println(new2)

}
