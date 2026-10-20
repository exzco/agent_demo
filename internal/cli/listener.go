// Package cli renders the agent's streaming transcript on a raw terminal:
// spinner, collapsible reasoning/tool-output details, mutating-tool approval
// gate and Ctrl+O detail repaints.
package cli

import (
	"agy/internal/subagent"
	"agy/internal/tools"
	"agy/internal/trajectory"
	"agy/internal/tui"
	"agy/internal/types"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

type Listener struct {
	App          *tui.App
	Spinner      *tui.Spinner
	RequireAsked bool // when true, mutating tools require interactive approval
	Out          io.Writer
	In           *os.File

	// mu serializes transcript mutation between agent callbacks and the
	// Ctrl+O repaint.
	mu sync.Mutex

	// toolLineRows is the distance (in terminal rows) from the cursor back to
	// the pending tool-call row, so the result can rewrite its dot in place.
	// Zero means no pending row (non-TTY or already settled).
	toolLineRows int

	// toolRow caches the rendered pending row so the settled row keeps the
	// exact same verb/argument presentation (file line spans may no longer
	// resolve once a mutating tool has run).
	toolRow string

	// detailsExpanded is the sticky expansion state of collapsible details
	// (reasoning blocks and tool outputs); Ctrl+O flips it and repaints the
	// turn.
	detailsExpanded bool

	// reasoning accumulation for the current step.
	reasoningText   strings.Builder
	reasoningStart  time.Time
	reasoningActive bool

	// turnRows buffers every transcript row of the current turn so Ctrl+O can
	// repaint the turn in place with details expanded or collapsed.
	turnRows         []*turnEntry
	turnFallbackRows int
	answerEntry      *turnEntry
	toolEntry        *turnEntry
}

// turnEntryKind discriminates buffered transcript rows.
type turnEntryKind int

const (
	turnLiteral turnEntryKind = iota // a plain printed row
	turnThought                      // a collapsible reasoning block
	turnToolRow                      // a tool-call row (dot settles in place)
)

// turnEntry is one logical row of the current turn transcript.
type turnEntry struct {
	kind      turnEntryKind
	text      string
	thought   *thoughtBlock
	result    string
	toolErr   error
	hasResult bool
}

// thoughtBlock is a finished reasoning stream shown as a collapsible block.
type thoughtBlock struct {
	text    string
	elapsed time.Duration
	tokens  int
}

// tty reports whether the listener writes to an interactive terminal.
func (l *Listener) tty() bool {
	f, ok := l.Out.(*os.File)
	return ok && tui.IsTerminal(f)
}

// width returns the terminal width for the output writer.
func (l *Listener) width() int {
	if f, ok := l.Out.(*os.File); ok {
		return tui.TerminalWidth(f)
	}
	return 80
}

// rowsUsed reports how many terminal rows the printed string occupies,
// counting wrapped lines and empty lines.
func rowsUsed(s string, width int) int {
	if s == "" {
		return 0
	}
	if width < 1 {
		width = 80
	}
	rows := 0
	for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		w := tui.DisplayWidth(line)
		if w < 1 {
			w = 1
		}
		rows += (w + width - 1) / width
	}
	return rows
}

// confirm asks the user to approve a gated tool call, then erases the
// transient approval UI (diff modal rows and prompt) so the decision does not
// linger in the conversation. The cursor is left right below the pending
// tool-call row, keeping it rewritable by OnToolResult.
func (l *Listener) confirm(prompt string) (bool, error) {
	if l.Spinner != nil {
		l.Spinner.Stop()
	}
	ok, rows, err := tui.ConfirmKeyPromptTracked(l.In, l.Out, prompt)
	if err != nil {
		return false, err
	}
	if !l.tty() {
		if ok && l.Spinner != nil {
			l.Spinner.Start("running…")
		}
		return ok, nil
	}
	total := (l.toolLineRows - 1) + rows
	l.toolLineRows = 1
	if total <= 0 {
		if ok && l.Spinner != nil {
			l.Spinner.Start("running…")
		}
		return ok, nil
	}
	fmt.Fprintf(l.Out, "\x1b[%dA", total)
	for i := 0; i < total; i++ {
		fmt.Fprint(l.Out, "\r\x1b[2K")
		if i < total-1 {
			fmt.Fprint(l.Out, "\x1b[1B")
		}
	}
	if total > 1 {
		fmt.Fprintf(l.Out, "\x1b[%dA", total-1)
	}
	if ok && l.Spinner != nil {
		l.Spinner.Start("running…")
	}
	return ok, nil
}

// BeginTurn resets the per-turn transcript buffer. It is called whenever the
// agent is about to write and whenever output may have bypassed the buffer
// (slash command results), so a stale buffer can never mis-anchor a Ctrl+O
// repaint.
func (l *Listener) BeginTurn() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.turnRows = nil
	l.turnFallbackRows = 0
	l.reasoningActive = false
	l.reasoningText.Reset()
	l.answerEntry = nil
	l.toolEntry = nil
	if l.App != nil && l.Spinner != nil && l.Spinner.Footer() == nil {
		l.Spinner.SetFooter(func() []string {
			if sb := l.App.RenderStatusBar(); sb != "" {
				return []string{sb}
			}
			return nil
		})
	}
}

func (l *Listener) appendRowLocked(kind turnEntryKind, text string) *turnEntry {
	e := &turnEntry{kind: kind, text: text}
	l.turnRows = append(l.turnRows, e)
	return e
}

// PrintTurnLine prints one transcript row and records it in the turn buffer
// so Ctrl+O can repaint it later.
func (l *Listener) PrintTurnLine(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintln(l.Out, text)
	l.appendRowLocked(turnLiteral, text)
}

// OnReasoning accumulates streamed reasoning_content deltas.
func (l *Listener) OnReasoning(delta string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.reasoningActive {
		l.reasoningActive = true
		l.reasoningStart = time.Now()
	}
	l.reasoningText.WriteString(delta)
}

// finishThinkingLocked ends the current thinking phase. When reasoning
// content was streamed it is swapped in place for a thought block; when no
// reasoning arrived at all the frozen spinner row is simply erased so the
// subsequent content (answer tokens or a tool call row) prints directly in
// its place. It reports whether a live row existed and was consumed; callers
// use that to decide whether to terminate the line with their own newline.
// The caller must hold l.mu.
func (l *Listener) finishThinkingLocked() bool {
	if l.reasoningActive {
		l.reasoningActive = false
		text := l.reasoningText.String()
		l.reasoningText.Reset()
		if strings.TrimSpace(text) != "" {
			block := &thoughtBlock{
				text:    text,
				elapsed: time.Since(l.reasoningStart),
			}
			block.tokens = tui.EstimateTokens(block.text)
			// Swap the frozen spinner row for the thought block in place.
			if l.Spinner != nil && l.Spinner.Final() && l.tty() {
				fmt.Fprint(l.Out, "\x1b[1A\r\x1b[2K")
			}
			if l.tty() {
				for _, row := range l.renderThoughtRows(block) {
					fmt.Fprintln(l.Out, row)
				}
			} else {
				fmt.Fprintln(l.Out, tui.ThoughtHeader(block.elapsed, block.tokens, false))
			}
			l.turnRows = append(l.turnRows, &turnEntry{kind: turnThought, thought: block})
			return true
		}
	}
	if l.Spinner != nil && l.Spinner.Final() {
		if l.tty() {
			fmt.Fprint(l.Out, "\x1b[1A\r\x1b[2K")
		}
		return true
	}
	return false
}

// renderThoughtRows renders one thought block in the current expansion state.
func (l *Listener) renderThoughtRows(b *thoughtBlock) []string {
	if l.detailsExpanded {
		return tui.RenderThoughtExpanded(b.text, b.elapsed, b.tokens, l.width())
	}
	return tui.RenderThoughtCollapsed(b.text, b.elapsed, b.tokens, l.width())
}

func (l *Listener) OnToken(token string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.finishThinkingLocked()
	if l.answerEntry == nil {
		l.answerEntry = l.appendRowLocked(turnLiteral, "")
	}
	l.answerEntry.text += token
	fmt.Fprint(l.Out, token)
}

func (l *Listener) OnToolCall(call types.ToolCall) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.finishThinkingLocked() {
		fmt.Fprint(l.Out, "\n")
	}
	l.toolRow = tui.RenderToolCall(call.Function.Name, call.Function.Arguments, l.width())
	fmt.Fprintln(l.Out, l.toolRow)
	l.toolEntry = l.appendRowLocked(turnToolRow, l.toolRow)
	l.answerEntry = nil
	l.toolLineRows = 0
	if l.tty() {
		l.toolLineRows = 1
	}
	if l.Spinner != nil && !l.RequireAsked {
		l.Spinner.Start("running…")
	}
}

func (l *Listener) OnToolResult(call types.ToolCall, result string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Spinner != nil {
		l.Spinner.Stop()
	}

	// Settle the pending call row in place: the hollow white dot turns into a
	// solid green dot on success and a solid red dot on failure.
	if up := l.toolLineRows; up > 0 {
		settled := tui.SettleToolCall(l.toolRow, err == nil)
		if l.toolRow == "" {
			settled = tui.RenderToolCallResult(call.Function.Name, call.Function.Arguments, err == nil, l.width())
		}
		fmt.Fprintf(l.Out, "\x1b[%dA\r\x1b[2K", up)
		fmt.Fprint(l.Out, settled)
		fmt.Fprintf(l.Out, "\x1b[%dB\r", up)
		if l.toolEntry != nil {
			l.toolEntry.text = settled
		}
	}
	l.toolLineRows = 0

	if l.toolEntry != nil {
		l.toolEntry.result = result
		l.toolEntry.toolErr = err
		l.toolEntry.hasResult = true
	}

	// Tool output is hidden by default in interactive mode; Ctrl+O repaints
	// the turn with the full payload. Non-TTY runs (e.g. -p piped into logs)
	// keep the full output visible because they have no Ctrl+O affordance.
	expanded := l.detailsExpanded || !l.tty()
	for _, line := range l.renderToolDetailRows(l.toolEntry, expanded) {
		fmt.Fprintln(l.Out, line)
	}
	fmt.Fprintln(l.Out)
	l.appendRowLocked(turnLiteral, "")
	if l.Spinner != nil {
		l.Spinner.Start("thinking…")
	}
}

func (l *Listener) OnStepComplete(step trajectory.Step) {}

// OnCompaction surfaces a context-compaction checkpoint on the transcript.
func (l *Listener) OnCompaction(beforeTokens, afterTokens int) {
	l.PrintTurnLine(fmt.Sprintf("\x1b[90m⛁ Context compacted: %d → %d tokens\x1b[0m", beforeTokens, afterTokens))
}

func (l *Listener) OnFinish(reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.finishThinkingLocked() {
		fmt.Fprint(l.Out, "\n")
	}
	line := fmt.Sprintf("\x1b[90m[Agent finished: %s]\x1b[0m", reason)
	fmt.Fprintln(l.Out, line)
	l.appendRowLocked(turnLiteral, line)
}

// eraseRows clears n rows ending at the cursor row and leaves the cursor at
// the top cleared row.
func (l *Listener) eraseRows(n int) {
	if n <= 0 {
		return
	}
	fmt.Fprintf(l.Out, "\x1b[%dA", n)
	for i := 0; i < n; i++ {
		fmt.Fprint(l.Out, "\r\x1b[2K")
		if i < n-1 {
			fmt.Fprint(l.Out, "\x1b[1B")
		}
	}
	fmt.Fprintf(l.Out, "\x1b[%dA", n-1)
}

// renderToolDetailRows renders a tool result either folded (default) or in
// full (Ctrl+O / non-TTY). A nil entry or a still-pending call yields nothing.
func (l *Listener) renderToolDetailRows(e *turnEntry, expanded bool) []string {
	if e == nil || e.kind != turnToolRow || !e.hasResult {
		return nil
	}
	if expanded {
		return tui.RenderToolOutputExpanded(e.result, e.toolErr, l.width())
	}
	return tui.RenderToolOutputCollapsed(e.result, e.toolErr, l.width())
}

// renderTurnRowsLocked renders every buffered row with the current expansion
// state. The caller must hold l.mu.
func (l *Listener) renderTurnRowsLocked() []string {
	var out []string
	for _, e := range l.turnRows {
		switch e.kind {
		case turnThought:
			out = append(out, l.renderThoughtRows(e.thought)...)
		case turnToolRow:
			out = append(out, e.text)
			out = append(out, l.renderToolDetailRows(e, l.detailsExpanded)...)
		default:
			out = append(out, e.text)
		}
	}
	return out
}

// turnRowCountLocked reports how many physical terminal rows the buffered
// turn occupies, counting wrapped lines. The caller must hold l.mu.
func (l *Listener) turnRowCountLocked() int {
	w := l.width()
	if w < 1 {
		w = 80
	}
	rows := 0
	for _, line := range l.renderTurnRowsLocked() {
		dw := tui.DisplayWidth(line)
		if dw < 1 {
			rows++
			continue
		}
		rows += (dw + w - 1) / w
	}
	return rows
}

// ToggleDetails is the Ctrl+O handler: it repaints the buffered turn above
// the input row with tool outputs and reasoning blocks toggled between
// collapsed and expanded. It reports whether the transcript above the cursor
// was fully repainted and the caller must reprint its divider row. When the
// turn is taller than the screen it degrades to printing a copy of the most
// recent detail block below; the next press removes that copy.
func (l *Listener) ToggleDetails(extraAbove int) bool {
	if !l.tty() {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	// A fallback copy is showing: the second press collapses it again.
	if l.turnFallbackRows > 0 {
		l.eraseRows(l.turnFallbackRows)
		l.turnFallbackRows = 0
		return false
	}

	hasDetail := false
	for _, e := range l.turnRows {
		if e.kind == turnThought {
			hasDetail = true
			break
		}
		if e.kind == turnToolRow && e.hasResult && (e.result != "" || e.toolErr != nil) {
			hasDetail = true
			break
		}
	}
	if !hasDetail {
		return false
	}

	rows := l.turnRowCountLocked()
	height := 24
	if f, ok := l.Out.(*os.File); ok {
		height = tui.TerminalHeight(f)
	}
	if rows+extraAbove > height-3 {
		l.printDetailFallbackLocked()
		return false
	}

	l.detailsExpanded = !l.detailsExpanded
	l.eraseRows(rows + extraAbove)
	for _, line := range l.renderTurnRowsLocked() {
		fmt.Fprintln(l.Out, line)
	}
	return true
}

// ToggleThoughts is retained as a compatibility alias; Ctrl+O now toggles all
// collapsible details, including reasoning blocks.
func (l *Listener) ToggleThoughts(extraAbove int) bool {
	return l.ToggleDetails(extraAbove)
}

// printDetailFallbackLocked prints the most recent detail block in full below
// the transcript when the turn no longer fits on screen. The caller must hold
// l.mu.
func (l *Listener) printDetailFallbackLocked() {
	var lastThought *thoughtBlock
	var lastTool *turnEntry
	for _, e := range l.turnRows {
		switch e.kind {
		case turnThought:
			lastThought = e.thought
			lastTool = nil
		case turnToolRow:
			if e.hasResult && (e.result != "" || e.toolErr != nil) {
				lastTool = e
				lastThought = nil
			}
		}
	}
	if lastThought == nil && lastTool == nil {
		return
	}

	var rows []string
	if lastTool != nil {
		rows = append(rows, lastTool.text)
		rows = append(rows, tui.RenderToolOutputExpanded(lastTool.result, lastTool.toolErr, l.width())...)
	} else {
		rows = tui.RenderThoughtExpanded(lastThought.text, lastThought.elapsed, lastThought.tokens, l.width())
	}
	for _, row := range rows {
		fmt.Fprintln(l.Out, row)
	}
	l.turnFallbackRows = rowsUsed(strings.Join(rows, "\n"), l.width())
}

// ApproveToolCall implements agent.ToolApprover. Only mutating tools are gated.
// The listener lock is held for the whole approval so a Ctrl+O repaint cannot
// interleave into the blocking confirmation prompt.
func (l *Listener) ApproveToolCall(call types.ToolCall) (bool, error) {
	if !l.RequireAsked {
		return true, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	switch call.Function.Name {
	case "write_to_file":
		var args tools.WriteFileArgs
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return false, fmt.Errorf("invalid write_to_file arguments: %w", err)
		}
		oldContent := ""
		if b, rerr := os.ReadFile(args.TargetFile); rerr == nil {
			oldContent = string(b)
		}
		newContent := args.CodeContent
		if args.Append {
			newContent = oldContent + args.CodeContent
		}
		modal := l.App.RenderDiffModal(args.TargetFile, oldContent, newContent)
		fmt.Fprint(l.Out, modal)
		l.toolLineRows += rowsUsed(modal, l.width())
		return l.confirm("Apply write_to_file?")

	case "replace_file_content":
		var args tools.EditFileArgs
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return false, fmt.Errorf("invalid replace_file_content arguments: %w", err)
		}
		oldContent := ""
		if b, rerr := os.ReadFile(args.TargetFile); rerr == nil {
			oldContent = string(b)
		}
		newContent := oldContent
		if args.AllowMultiple {
			newContent = strings.ReplaceAll(oldContent, args.TargetContent, args.ReplacementContent)
		} else {
			newContent = strings.Replace(oldContent, args.TargetContent, args.ReplacementContent, 1)
		}
		modal := l.App.RenderDiffModal(args.TargetFile, oldContent, newContent)
		fmt.Fprint(l.Out, modal)
		l.toolLineRows += rowsUsed(modal, l.width())
		return l.confirm("Apply replace_file_content?")

	case "run_command":
		var args tools.RunCommandArgs
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return false, fmt.Errorf("invalid run_command arguments: %w", err)
		}
		return l.confirm("Execute command?")

	case "invoke_subagent":
		var args subagent.InvokeSubagentArgs
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return false, fmt.Errorf("invalid invoke_subagent arguments: %w", err)
		}
		block := "\n\x1b[1;33m⇢ Spawn subagents:\x1b[0m\n"
		for _, s := range args.Subagents {
			prompt := s.Prompt
			if len(prompt) > 90 {
				prompt = prompt[:87] + "..."
			}
			block += fmt.Sprintf("  • %-16s %s\n", s.Role, prompt)
		}
		fmt.Fprint(l.Out, block)
		l.toolLineRows += rowsUsed(block, l.width())
		return l.confirm("Spawn subagents?")
	}

	// Read-only tools (read_file) run without confirmation.
	if l.Spinner != nil {
		l.Spinner.Start("running…")
	}
	return true, nil
}
