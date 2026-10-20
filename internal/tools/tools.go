package tools

import (
	"agy/internal/types"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Tool interface {
	Name() string
	Description() string
	ParameterSchema() map[string]any
	Execute(ctx context.Context, argsJSON []byte) (string, error)
}

// ----------------------------------------------------------------------
// ReadFileTool
// ----------------------------------------------------------------------

type ReadFileTool struct{}

func (t *ReadFileTool) Name() string { return "read_file" }
func (t *ReadFileTool) Description() string {
	return "View the contents of a file from the local filesystem with line range slicing."
}
func (t *ReadFileTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"AbsolutePath": map[string]any{"type": "string", "description": "Absolute path to the file"},
			"StartLine":    map[string]any{"type": "integer", "description": "1-indexed start line"},
			"EndLine":      map[string]any{"type": "integer", "description": "1-indexed end line"},
		},
		"required": []string{"AbsolutePath"},
	}
}

type ReadFileArgs struct {
	AbsolutePath string `json:"AbsolutePath"`
	StartLine    int    `json:"StartLine,omitempty"`
	EndLine      int    `json:"EndLine,omitempty"`
}

func (t *ReadFileTool) Execute(ctx context.Context, argsJSON []byte) (string, error) {
	var args ReadFileArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return "", fmt.Errorf("read_file: failed to unmarshal args: %w", err)
	}
	if args.AbsolutePath == "" {
		return "", fmt.Errorf("read_file: AbsolutePath must not be empty")
	}
	info, err := os.Stat(args.AbsolutePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("read_file: file does not exist: %s", args.AbsolutePath)
		}
		return "", fmt.Errorf("read_file: failed to stat file: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("read_file: %s is a directory, not a file", args.AbsolutePath)
	}

	f, err := os.Open(args.AbsolutePath)
	if err != nil {
		return "", fmt.Errorf("read_file: failed to open file: %w", err)
	}
	defer f.Close()

	var lines []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		lines = append(lines, s.Text())
	}
	if err := s.Err(); err != nil {
		return "", fmt.Errorf("read_file: error reading file: %w", err)
	}

	total := len(lines)
	start := 1
	if args.StartLine > 0 {
		start = args.StartLine
	}
	end := total
	if args.EndLine > 0 && args.EndLine < total {
		end = args.EndLine
	}
	if start > total {
		start = total
	}
	if start > end {
		return "", fmt.Errorf("read_file: StartLine %d must be <= EndLine %d", start, end)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("File: %s (%d lines total, showing %d-%d)\n", args.AbsolutePath, total, start, end))
	for i := start; i <= end; i++ {
		line := ""
		if i-1 < len(lines) {
			line = lines[i-1]
		}
		sb.WriteString(fmt.Sprintf("%d: %s\n", i, line))
	}
	return TruncateToolOutput(sb.String(), DefaultMaxToolOutputChars), nil
}

// ----------------------------------------------------------------------
// WriteFileTool
// ----------------------------------------------------------------------

type WriteFileTool struct{}

func (t *WriteFileTool) Name() string { return "write_to_file" }
func (t *WriteFileTool) Description() string {
	return "Create a new file or append to an existing file. Fails if file exists unless Overwrite or Append is true."
}
func (t *WriteFileTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"TargetFile":  map[string]any{"type": "string", "description": "Absolute path to the target file"},
			"CodeContent": map[string]any{"type": "string", "description": "Content to write"},
			"Overwrite":   map[string]any{"type": "boolean", "description": "Overwrite existing file"},
			"Append":      map[string]any{"type": "boolean", "description": "Append to existing file"},
		},
		"required": []string{"TargetFile", "CodeContent"},
	}
}

type WriteFileArgs struct {
	TargetFile  string `json:"TargetFile"`
	CodeContent string `json:"CodeContent"`
	Overwrite   bool   `json:"Overwrite,omitempty"`
	Append      bool   `json:"Append,omitempty"`
}

func (t *WriteFileTool) Execute(ctx context.Context, argsJSON []byte) (string, error) {
	var args WriteFileArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return "", fmt.Errorf("write_to_file: failed to unmarshal args: %w", err)
	}
	if args.TargetFile == "" {
		return "", fmt.Errorf("Path must not be empty")
	}
	if args.Overwrite && args.Append {
		return "", fmt.Errorf("Overwrite and Append cannot both be true")
	}

	exists := false
	if info, err := os.Stat(args.TargetFile); err == nil {
		exists = true
		if info.IsDir() {
			return "", fmt.Errorf("%s is a directory, not a file", args.TargetFile)
		}
	}

	if exists && !args.Overwrite && !args.Append {
		return "", fmt.Errorf("%s already exists. To overwrite it, retry with Overwrite=true. To edit it, view its contents first and use a code edit tool. Otherwise, choose a different file name", args.TargetFile)
	}

	dir := filepath.Dir(args.TargetFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	flag := os.O_WRONLY | os.O_CREATE
	if args.Append {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}

	f, err := os.OpenFile(args.TargetFile, flag, 0644)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(args.CodeContent); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	action := "Created"
	if exists {
		if args.Append {
			action = "Appended to"
		} else {
			action = "Overwrote"
		}
	}
	return fmt.Sprintf("%s file %s successfully (%d bytes written).", action, args.TargetFile, len(args.CodeContent)), nil
}

// ----------------------------------------------------------------------
// EditFileTool
// ----------------------------------------------------------------------

type EditFileTool struct{}

func (t *EditFileTool) Name() string { return "replace_file_content" }
func (t *EditFileTool) Description() string {
	return "Edit an existing file by replacing a contiguous target block of text."
}
func (t *EditFileTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"TargetFile":         map[string]any{"type": "string", "description": "Absolute path to file"},
			"TargetContent":      map[string]any{"type": "string", "description": "Exact text to replace"},
			"ReplacementContent": map[string]any{"type": "string", "description": "Replacement text"},
			"AllowMultiple":      map[string]any{"type": "boolean", "description": "Replace multiple instances"},
		},
		"required": []string{"TargetFile", "TargetContent", "ReplacementContent"},
	}
}

type EditFileArgs struct {
	TargetFile         string `json:"TargetFile"`
	TargetContent      string `json:"TargetContent"`
	ReplacementContent string `json:"ReplacementContent"`
	AllowMultiple      bool   `json:"AllowMultiple,omitempty"`
}

func (t *EditFileTool) Execute(ctx context.Context, argsJSON []byte) (string, error) {
	var args EditFileArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return "", fmt.Errorf("failed to unmarshal args: %w", err)
	}
	if args.TargetFile == "" {
		return "", fmt.Errorf("Target must not be empty")
	}

	info, err := os.Stat(args.TargetFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%s does not exist; the file may have been moved, renamed, or deleted", args.TargetFile)
		}
		return "", fmt.Errorf("failed to stat file: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not a file", args.TargetFile)
	}

	contentBytes, err := os.ReadFile(args.TargetFile)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	content := string(contentBytes)

	count := strings.Count(content, args.TargetContent)
	if count == 0 {
		return "", fmt.Errorf("TargetContent was not found in %s", args.TargetFile)
	}
	if count > 1 && !args.AllowMultiple {
		return "", fmt.Errorf("TargetContent occurred %d times in %s. Specify AllowMultiple=true or provide a more unique target block", count, args.TargetFile)
	}

	firstIndex := strings.Index(content, args.TargetContent)
	startLine := 1 + strings.Count(content[:firstIndex], "\n")

	oldTargetLines := strings.Count(args.TargetContent, "\n")
	newTargetLines := strings.Count(args.ReplacementContent, "\n")
	addLines := 0
	subLines := 0
	if newTargetLines >= oldTargetLines {
		addLines = newTargetLines - oldTargetLines
	} else {
		subLines = oldTargetLines - newTargetLines
	}

	var newContent string
	var replacementsMade int
	if args.AllowMultiple {
		newContent = strings.ReplaceAll(content, args.TargetContent, args.ReplacementContent)
		replacementsMade = count
	} else {
		newContent = strings.Replace(content, args.TargetContent, args.ReplacementContent, 1)
		replacementsMade = 1
	}

	if err := os.WriteFile(args.TargetFile, []byte(newContent), info.Mode()); err != nil {
		return "", fmt.Errorf("failed to write updated file: %w", err)
	}

	plural := "replacement"
	if replacementsMade > 1 {
		plural = "replacements"
	}
	return fmt.Sprintf("Made %d %s in %s starting at line %d (+%d/-%d lines).",
		replacementsMade, plural, args.TargetFile, startLine, addLines, subLines), nil
}

// ----------------------------------------------------------------------
// RunCommandTool
// ----------------------------------------------------------------------

type RunCommandTool struct {
	defaultCwd string
}

func (t *RunCommandTool) Name() string { return "run_command" }
func (t *RunCommandTool) Description() string {
	osStr := runtime.GOOS
	shell := "bash"
	if osStr == "windows" {
		shell = "cmd.exe"
	}
	return fmt.Sprintf("Execute a command in the workspace. Operating System: %s. Shell: %s.", osStr, shell)
}
func (t *RunCommandTool) ParameterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"CommandLine":       map[string]any{"type": "string", "description": "The command line string to execute"},
			"Cwd":               map[string]any{"type": "string", "description": "Working directory"},
			"WaitMsBeforeAsync": map[string]any{"type": "integer", "description": "Timeout in milliseconds"},
		},
		"required": []string{"CommandLine"},
	}
}

type RunCommandArgs struct {
	CommandLine       string `json:"CommandLine"`
	Cwd               string `json:"Cwd,omitempty"`
	WaitMsBeforeAsync int    `json:"WaitMsBeforeAsync,omitempty"`
}

func (t *RunCommandTool) Execute(ctx context.Context, argsJSON []byte) (string, error) {
	var args RunCommandArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return "", fmt.Errorf("run_command: failed to unmarshal args: %w", err)
	}
	if strings.TrimSpace(args.CommandLine) == "" {
		return "", fmt.Errorf("run_command: invalid arguments: CommandLine must not be empty")
	}

	cwd := args.Cwd
	if cwd == "" {
		cwd = t.defaultCwd
	}

	timeout := 60 * time.Second
	if args.WaitMsBeforeAsync > 0 {
		timeout = time.Duration(args.WaitMsBeforeAsync) * time.Millisecond
	}

	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(cmdCtx, "cmd.exe", "/c", args.CommandLine)
	} else {
		cmd = exec.CommandContext(cmdCtx, "bash", "-c", args.CommandLine)
	}

	if cwd != "" {
		cmd.Dir = cwd
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	var exitCode int
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				return "", fmt.Errorf("command was terminated by a signal: %w", runErr)
			}
		} else if cmdCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("Command timed out: %w", cmdCtx.Err())
		} else {
			return "", fmt.Errorf("Command failed to run: %w", runErr)
		}
	}

	var sb strings.Builder
	if stdout.Len() > 0 {
		sb.WriteString(stdout.String())
	}
	if stderr.Len() > 0 {
		if sb.Len() > 0 && !strings.HasSuffix(sb.String(), "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("Stderr:\n")
		sb.WriteString(stderr.String())
	}
	if sb.Len() > 0 && !strings.HasSuffix(sb.String(), "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("· Exited with code %d", exitCode))
	return TruncateToolOutput(sb.String(), DefaultMaxToolOutputChars), nil
}

// DefaultMaxToolOutputChars defines the upper bound of a single tool execution's
// returned text content (approx. 800-1000 tokens). Outputs exceeding this limit
// are truncated using head-tail retention with a standardized spill notice.
// Restricting delta output volume is crucial for preserving high KV cache hit rates (90%+).
const DefaultMaxToolOutputChars = 3000

// TruncateToolOutput preserves head and tail slices of large tool outputs, spills
// the complete un-truncated output to a persistent local temp file, and inserts a
// notice instructing the agent where to find the complete log or how to slice it.
func TruncateToolOutput(output string, maxChars int) string {
	if maxChars <= 0 {
		maxChars = DefaultMaxToolOutputChars
	}
	if len(output) <= maxChars {
		return output
	}

	headLen := maxChars / 2
	tailLen := maxChars / 2
	if headLen > len(output) {
		headLen = len(output)
	}
	if headLen+tailLen > len(output) {
		tailLen = len(output) - headLen
	}

	head := output[:headLen]
	tail := output[len(output)-tailLen:]
	omittedBytes := len(output) - headLen - tailLen
	omittedLines := strings.Count(output[headLen:len(output)-tailLen], "\n")

	// Spill the full content to a local temporary file (DSH / Codex style spill).
	// Using a deterministic content SHA256 hash instead of a random or nanosecond
	// timestamp prevents cache thrashing and preserves byte-for-byte prefix stability.
	h := sha256.Sum256([]byte(output))
	hashHex := hex.EncodeToString(h[:])[:12]
	spillFile := filepath.Join(os.TempDir(), fmt.Sprintf("agy-tool-output-%s.log", hashHex))
	_ = os.WriteFile(spillFile, []byte(output), 0644)

	var sb strings.Builder
	sb.WriteString(head)
	sb.WriteString(fmt.Sprintf("\n\n[... Omitted %d bytes (%d lines). Truncated to protect token budget. Full output stored at: %s ...]\n\n", omittedBytes, omittedLines, spillFile))
	sb.WriteString(tail)
	return sb.String()
}

// ----------------------------------------------------------------------
// Registry
// ----------------------------------------------------------------------

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry {
	r := &Registry{tools: make(map[string]Tool)}
	r.Register(&ReadFileTool{})
	r.Register(&WriteFileTool{})
	r.Register(&EditFileTool{})
	r.Register(&RunCommandTool{defaultCwd: "."})
	return r
}

func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// ExportOpenAITools exports registered tools deterministically sorted by name.
// Deterministic ordering is essential for Prompt Caching (KV cache reuse):
// if tools change order across requests, the prefix hash fails and busts the cache.
func (r *Registry) ExportOpenAITools() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	res := make([]map[string]any, 0, len(names))
	for _, name := range names {
		t := r.tools[name]
		res = append(res, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name(),
				"description": t.Description(),
				"parameters":  t.ParameterSchema(),
			},
		})
	}
	return res
}

func (r *Registry) Execute(ctx context.Context, call types.ToolCall) (string, error) {
	tool, ok := r.Get(call.Function.Name)
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", call.Function.Name)
	}
	return tool.Execute(ctx, []byte(call.Function.Arguments))
}
