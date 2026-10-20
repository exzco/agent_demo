package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// thoughtPreviewLines is how many transcript lines of a collapsed thought
// block are shown below its header.
const thoughtPreviewLines = 2

// ThoughtHeader renders the one-row header of a collapsible reasoning block:
// "▸ Thought for 17s, 2.1k tokens" collapsed, "▾ ..." when expanded.
func ThoughtHeader(elapsed time.Duration, tokens int, expanded bool) string {
	marker := "▸"
	if expanded {
		marker = "▾"
	}
	header := fmt.Sprintf("Thought for %s, %s tokens", formatThoughtElapsed(elapsed), FormatTokenCount(tokens))
	return ansi("90", marker) + " " + header
}

// formatThoughtElapsed renders a duration the way the TUI shows it: "0.9s"
// for sub-second phases, whole seconds up to one minute ("17s"), and
// "1m 27s" from there on.
func formatThoughtElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	s := int(d.Round(time.Second) / time.Second)
	if s < 60 {
		return strconv.Itoa(s) + "s"
	}
	return fmt.Sprintf("%dm %ds", s/60, s%60)
}

// EstimateTokens approximates the token count of a text without a tokenizer:
// ASCII text averages roughly four characters per token while non-ASCII runes
// (CJK and friends) are counted one token each.
func EstimateTokens(s string) int {
	ascii, wide := 0, 0
	for _, r := range s {
		if r < 0x80 {
			ascii++
		} else {
			wide++
		}
	}
	return (ascii+3)/4 + wide
}

// FormatTokenCount renders a token count the way the TUI displays it: the
// plain number below one thousand, "2.1k" from there on.
func FormatTokenCount(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return strconv.Itoa(n)
}

// RenderThoughtCollapsed renders a reasoning block in its default collapsed
// state: the header plus up to two dimmed preview rows truncated to width.
func RenderThoughtCollapsed(text string, elapsed time.Duration, tokens, width int) []string {
	rows := []string{ThoughtHeader(elapsed, tokens, false)}
	shown := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if width > 4 {
			line = truncateANSI(line, width-2)
		}
		rows = append(rows, "  "+ansi("90", line))
		shown++
		if shown == thoughtPreviewLines {
			break
		}
	}
	return rows
}

// RenderThoughtExpanded renders the full reasoning text hard-wrapped to
// width, indented and dimmed below the expanded header.
func RenderThoughtExpanded(text string, elapsed time.Duration, tokens, width int) []string {
	rows := []string{ThoughtHeader(elapsed, tokens, true)}
	body := width - 2
	if body < 8 {
		body = 8
	}
	for _, line := range wrapByColumns(strings.TrimRight(text, "\n"), body) {
		if strings.TrimSpace(line) == "" {
			rows = append(rows, "")
			continue
		}
		rows = append(rows, "  "+ansi("90", line))
	}
	return rows
}

// wrapByColumns hard-wraps text to at most cols display columns per row,
// keeping CJK wide runes intact.
func wrapByColumns(text string, cols int) []string {
	var out []string
	for _, logical := range strings.Split(text, "\n") {
		logical = strings.TrimRight(logical, "\r")
		if logical == "" {
			out = append(out, "")
			continue
		}
		var cur []rune
		w := 0
		for _, r := range logical {
			rw := runeDisplayWidth(r)
			if w+rw > cols && len(cur) > 0 {
				out = append(out, string(cur))
				cur = cur[:0]
				w = 0
			}
			cur = append(cur, r)
			w += rw
		}
		if len(cur) > 0 {
			out = append(out, string(cur))
		}
	}
	return out
}
