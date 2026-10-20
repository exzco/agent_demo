package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// toolCallStyle describes how one tool invocation is rendered in the compact
// "● Verb(arg)" terminal style: the verb shown and which argument keys to
// probe (first non-empty wins).
type toolCallStyle struct {
	verb     string
	pick     []string
	lineSpan bool // replace_file_content: append :L{s}-L{e} when resolvable
}

var toolCallStyles = map[string]toolCallStyle{
	"view_file":            {verb: "Read", pick: []string{"AbsolutePath", "Path", "TargetFile"}},
	"read_file":            {verb: "Read", pick: []string{"AbsolutePath", "Path", "TargetFile"}},
	"write_to_file":        {verb: "Write", pick: []string{"TargetFile", "Path", "AbsolutePath"}},
	"replace_file_content": {verb: "Edit", pick: []string{"TargetFile", "Path", "AbsolutePath"}, lineSpan: true},
	"run_command":          {verb: "Bash", pick: []string{"CommandLine", "Command", "Cmd"}},
	"list_dir":             {verb: "ListDir", pick: []string{"AbsolutePath", "Path", "Directory"}},
	"find_by_name":         {verb: "Find", pick: []string{"Pattern", "Name"}},
	"grep_search":          {verb: "Grep", pick: []string{"Query", "SearchTerm", "Pattern"}},
	"invoke_subagent":      {verb: "Agent", pick: []string{"Role", "AgentRole"}},
	"search_web":           {verb: "WebSearch", pick: []string{"Query", "SearchTerm"}},
	"read_url_content":     {verb: "WebFetch", pick: []string{"Url", "URL", "AbsolutePath"}},
	"ask_question":         {verb: "Ask", pick: []string{"Question", "Query"}},
}

// fallbackToolArgs are probed for unknown tool names so custom tools still
// render a useful argument instead of an empty pair of parentheses.
var fallbackToolArgs = []string{"Path", "AbsolutePath", "TargetFile", "CommandLine", "Query", "Name", "Url", "Question", "Role"}

// RenderToolCall renders a pending tool invocation as a single row: a hollow
// white dot, a yellow verb and the parenthesized argument in the default
// color. The row is truncated to width columns when width > 0.
func RenderToolCall(name, argsJSON string, width int) string {
	return renderToolCall(name, argsJSON, ansi("37", "○"), width)
}

// RenderToolCallResult renders the settled variant of a tool-call row: the
// dot turns solid green when the call succeeded and solid red on failure.
func RenderToolCallResult(name, argsJSON string, ok bool, width int) string {
	return renderToolCall(name, argsJSON, settledDot(ok), width)
}

// SettleToolCall rewrites a pending row (produced by RenderToolCall) with the
// settled status dot: solid green on success, solid red on failure. The rest
// of the row is preserved verbatim, so file line spans keep the resolution
// they had while pending even after a mutating tool changed the file.
func SettleToolCall(pending string, ok bool) string {
	if rest, found := strings.CutPrefix(pending, ansi("37", "○")); found {
		return settledDot(ok) + rest
	}
	return settledDot(ok) + " " + pending
}

func settledDot(ok bool) string {
	if ok {
		return ansi("32", "●")
	}
	return ansi("31", "●")
}

func renderToolCall(name, argsJSON, dot string, width int) string {
	verb, arg := toolCallParts(name, argsJSON)

	// The dot carries the state (hollow white pending, solid green success,
	// solid red failure), the tool name is rendered in yellow, and the
	// parenthesized argument stays in the default text color.
	line := dot + " " + ansi("33", verb)
	if arg == "" {
		line += "()"
	} else {
		line += "(" + arg + ")"
	}
	if width > 4 {
		line = truncateANSI(line, width-1)
	}
	return line
}

// toolCallParts extracts the display verb and its single-line argument.
func toolCallParts(name, argsJSON string) (string, string) {
	style, known := toolCallStyles[name]
	if !known {
		style = toolCallStyle{verb: name, pick: fallbackToolArgs}
	}

	var args map[string]any
	if argsJSON != "" {
		_ = json.Unmarshal([]byte(argsJSON), &args)
	}

	// invoke_subagent nests the role inside a Subagents array; surface it as
	// a flat "Role" key so the generic picker can read it.
	if subs, ok := args["Subagents"].([]any); ok && len(subs) > 0 {
		if sub, ok := subs[0].(map[string]any); ok {
			if role, ok := sub["Role"].(string); ok && strings.TrimSpace(role) != "" {
				args["Role"] = role
			}
		}
	}

	arg := ""
	for _, key := range style.pick {
		v, ok := args[key]
		if !ok {
			continue
		}
		if s := strings.TrimSpace(fmt.Sprintf("%v", v)); s != "" {
			arg = s
			break
		}
	}

	if style.lineSpan && arg != "" {
		arg = appendLineSpan(arg, args)
	}

	return style.verb, sanitizeArg(arg)
}

// maxExpandedToolOutputLines bounds how many output rows Ctrl+O repaints for
// one tool call; larger outputs are truncated with an explicit notice.
const maxExpandedToolOutputLines = 200

// RenderToolOutputCollapsed renders the default one-row state of a tool
// result: a dim notice that output is hidden, or a compact error summary.
// The full payload is available through Ctrl+O (RenderToolOutputExpanded).
func RenderToolOutputCollapsed(result string, err error, width int) []string {
	result = strings.TrimRight(result, "\r\n")
	if err == nil && result == "" {
		return nil
	}
	if err != nil {
		msg := strings.TrimRight(err.Error(), "\r\n")
		lines := strings.Split(msg, "\n")
		row := "  " + ansi("31", "Error: "+sanitizeArg(lines[0]))
		if width > 4 {
			row = truncateANSI(row, width)
		}
		rows := []string{row}
		if extra := len(lines) - 1; extra > 0 {
			rows = append(rows, dimHint(fmt.Sprintf("  … %d more line(s) · Ctrl+O to expand", extra), width))
		}
		if result != "" {
			rows = append(rows, dimHint(hiddenOutputHint(result), width))
		}
		return rows
	}
	return []string{dimHint(hiddenOutputHint(result), width)}
}

// RenderToolOutputExpanded renders the full tool result (and error text)
// indented below its call row, capped to a safe repaint budget.
func RenderToolOutputExpanded(result string, err error, width int) []string {
	var rows []string
	if err != nil {
		for _, line := range strings.Split(strings.TrimRight(err.Error(), "\r\n"), "\n") {
			rows = append(rows, "  "+ansi("31", strings.TrimRight(line, "\r")))
		}
	}
	if result = strings.TrimRight(result, "\r\n"); result != "" {
		for _, line := range strings.Split(result, "\n") {
			rows = append(rows, "  "+strings.TrimRight(line, "\r"))
		}
	}
	if len(rows) == 0 {
		return nil
	}
	if len(rows) > maxExpandedToolOutputLines {
		hidden := len(rows) - maxExpandedToolOutputLines
		rows = rows[:maxExpandedToolOutputLines]
		rows = append(rows, dimHint(fmt.Sprintf("  … %d more line(s) not shown", hidden), width))
	}
	return rows
}

// hiddenOutputHint describes a folded tool result in one compact row.
func hiddenOutputHint(result string) string {
	n := strings.Count(result, "\n") + 1
	if n == 1 {
		return "  … output hidden · Ctrl+O to expand"
	}
	return fmt.Sprintf("  … %d lines hidden · Ctrl+O to expand", n)
}

// dimHint styles a collapsed-detail hint dim gray and bounds it to width.
func dimHint(s string, width int) string {
	if width > 4 {
		s = truncateANSI(s, width)
	}
	return ansi("90", s)
}

// appendLineSpan annotates path with the :L{start}-L{end} range covered by
// TargetContent inside the target file. The span is omitted silently when the
// file or the searched text cannot be read/found.
func appendLineSpan(path string, args map[string]any) string {
	content, _ := args["TargetContent"].(string)
	if content == "" {
		return path
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return path
	}
	idx := strings.Index(string(b), content)
	if idx < 0 {
		return path
	}
	start := strings.Count(string(b[:idx]), "\n") + 1
	end := start + strings.Count(content, "\n")
	return fmt.Sprintf("%s:L%d-L%d", path, start, end)
}

// sanitizeArg flattens the argument to one terminal-safe row: control
// characters (except tab) are dropped and newlines collapse to spaces.
func sanitizeArg(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case r < 32 || r == 127:
			return -1
		default:
			return r
		}
	}, s)
}
