package tui

import (
	"os"
	"strconv"
	"strings"
)

// IsTerminal reports whether f is attached to an interactive terminal.
func IsTerminal(f *os.File) bool {
	return isTerminal(f.Fd())
}

// TerminalWidth returns the usable terminal column count for f, preferring
// the OS console query, then $COLUMNS, then a safe default of 80.
func TerminalWidth(f *os.File) int {
	return resolveTerminalWidth(f)
}

// TerminalHeight returns the terminal row count for f, preferring the OS
// console query, then $LINES, then a safe default of 24.
func TerminalHeight(f *os.File) int {
	if h := consoleHeight(f.Fd()); h > 0 {
		return h
	}
	if l := strings.TrimSpace(os.Getenv("LINES")); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			return n
		}
	}
	return 24
}

// resolveTerminalWidth returns the usable terminal column count for the input
// editor, preferring the OS console query, then $COLUMNS, then a safe default.
func resolveTerminalWidth(in *os.File) int {
	if w := consoleWidth(in.Fd()); w > 0 {
		return w
	}
	if c := strings.TrimSpace(os.Getenv("COLUMNS")); c != "" {
		if n, err := strconv.Atoi(c); err == nil && n > 0 {
			return n
		}
	}
	return 80
}

// windowStart returns the first visible column of a single-row horizontal
// window over prompt+input text of the given total width, so that the cursor
// at column col stays visible. Called only when total > view.
func windowStart(total, col, view int) int {
	if total <= view {
		return 0
	}
	if col <= view-2 {
		return 0
	}
	start := col - (view - 2)
	if maxStart := total - view; start > maxStart {
		start = maxStart
	}
	if start < 0 {
		start = 0
	}
	return start
}
