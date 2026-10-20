package tui

import (
	"fmt"
	"strings"
)

// LineDiff represents a single diff line.
type LineDiff struct {
	Type    rune // ' ', '+', '-'
	OldLine int
	NewLine int
	Text    string
}

// DiffOptions controls unified diff rendering.
type DiffOptions struct {
	ContextLines   int // unchanged lines kept around each change (default 3)
	MaxLineWidth   int // visual truncation width for overly long lines (default 400, <=0 disables)
	MaxOutputLines int // hard cap on emitted diff lines (default 2000, <=0 disables)
	OmitHeader     bool
}

func defaultDiffOptions() DiffOptions {
	return DiffOptions{ContextLines: 3, MaxLineWidth: 400, MaxOutputLines: 2000}
}

// maxDiffCells bounds the O(N*M) LCS work. Beyond it we fall back to a coarse
// replace-region diff, which is always correct (never crashes), just coarser.
const maxDiffCells = 25_000_000

type editOp struct {
	kind byte // ' ', '+', '-'
	text string
}

// splitLines splits text into logical lines, dropping the trailing empty line
// produced by a final newline and treating empty input as zero lines.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// lcsRow returns res[j] = LCS length of a[0:] vs b[0:j], using O(len(b)) memory.
func lcsRow(a, b []string) []int {
	m := len(b)
	prev := make([]int, m+1)
	cur := make([]int, m+1)
	for i := 1; i <= len(a); i++ {
		cur[0] = 0
		ai := a[i-1]
		for j := 1; j <= m; j++ {
			switch {
			case ai == b[j-1]:
				cur[j] = prev[j-1] + 1
			case prev[j] >= cur[j-1]:
				cur[j] = prev[j]
			default:
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
	}
	return prev
}

func reversed(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[len(lines)-1-i] = l
	}
	return out
}

// computeEdits produces an ordered edit script transforming a into b.
func computeEdits(a, b []string) []editOp {
	if len(a)*len(b) > maxDiffCells {
		return coarseEdits(a, b)
	}
	var out []editOp
	hirschberg(a, b, &out)
	return out
}

// hirschberg computes the LCS-based edit script in O(N*M) time and
// O(min(N,M)) memory (Hirschberg's divide-and-conquer refinement).
func hirschberg(a, b []string, out *[]editOp) {
	// Trim common prefix.
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		*out = append(*out, editOp{' ', a[p]})
		p++
	}
	a, b = a[p:], b[p:]

	// Trim common suffix into a staging slice (emitted last, in order).
	c := 0
	for c < len(a) && c < len(b) && a[len(a)-1-c] == b[len(b)-1-c] {
		c++
	}
	var suffix []string
	if c > 0 {
		suffix = a[len(a)-c:]
		a, b = a[:len(a)-c], b[:len(b)-c]
	}

	switch {
	case len(a) == 0:
		for _, l := range b {
			*out = append(*out, editOp{'+', l})
		}
	case len(b) == 0:
		for _, l := range a {
			*out = append(*out, editOp{'-', l})
		}
	case len(a) == 1:
		found := indexOf(b, a[0])
		if found < 0 {
			*out = append(*out, editOp{'-', a[0]})
			for _, l := range b {
				*out = append(*out, editOp{'+', l})
			}
		} else {
			for j := 0; j < found; j++ {
				*out = append(*out, editOp{'+', b[j]})
			}
			*out = append(*out, editOp{' ', a[0]})
			for j := found + 1; j < len(b); j++ {
				*out = append(*out, editOp{'+', b[j]})
			}
		}
	case len(b) == 1:
		found := indexOf(a, b[0])
		if found < 0 {
			for _, l := range a {
				*out = append(*out, editOp{'-', l})
			}
			*out = append(*out, editOp{'+', b[0]})
		} else {
			for i := 0; i < found; i++ {
				*out = append(*out, editOp{'-', a[i]})
			}
			*out = append(*out, editOp{' ', b[0]})
			for i := found + 1; i < len(a); i++ {
				*out = append(*out, editOp{'-', a[i]})
			}
		}
	default:
		mid := len(a) / 2
		fwd := lcsRow(a[:mid], b)
		rev := lcsRow(reversed(a[mid:]), reversed(b))
		best, bestK := -1, 0
		for k := 0; k <= len(b); k++ {
			if score := fwd[k] + rev[len(b)-k]; score > best {
				best, bestK = score, k
			}
		}
		hirschberg(a[:mid], b[:bestK], out)
		hirschberg(a[mid:], b[bestK:], out)
	}

	for _, l := range suffix {
		*out = append(*out, editOp{' ', l})
	}
}

func indexOf(lines []string, target string) int {
	for i, l := range lines {
		if l == target {
			return i
		}
	}
	return -1
}

// coarseEdits is the O(N) safety valve for giant inputs: trim prefix/suffix and
// treat the remaining middle as a wholesale replace.
func coarseEdits(a, b []string) []editOp {
	var out []editOp
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		out = append(out, editOp{' ', a[p]})
		p++
	}
	a, b = a[p:], b[p:]

	c := 0
	for c < len(a) && c < len(b) && a[len(a)-1-c] == b[len(b)-1-c] {
		c++
	}
	var suffix []string
	if c > 0 {
		suffix = a[len(a)-c:]
		a, b = a[:len(a)-c], b[:len(b)-c]
	}

	for _, l := range a {
		out = append(out, editOp{'-', l})
	}
	for _, l := range b {
		out = append(out, editOp{'+', l})
	}
	for _, l := range suffix {
		out = append(out, editOp{' ', l})
	}
	return out
}

// numberedDiffs converts the edit script into LineDiff entries with 1-indexed
// old/new line numbers.
func numberedDiffs(edits []editOp) []LineDiff {
	diffs := make([]LineDiff, 0, len(edits))
	oldNo, newNo := 1, 1
	for _, e := range edits {
		switch e.kind {
		case ' ':
			diffs = append(diffs, LineDiff{Type: ' ', OldLine: oldNo, NewLine: newNo, Text: e.text})
			oldNo++
			newNo++
		case '-':
			diffs = append(diffs, LineDiff{Type: '-', OldLine: oldNo, Text: e.text})
			oldNo++
		case '+':
			diffs = append(diffs, LineDiff{Type: '+', NewLine: newNo, Text: e.text})
			newNo++
		}
	}
	return diffs
}

// DiffStats returns the number of added and removed lines between old and new.
func DiffStats(oldText, newText string) (added, removed int) {
	for _, e := range computeEdits(splitLines(oldText), splitLines(newText)) {
		switch e.kind {
		case '+':
			added++
		case '-':
			removed++
		}
	}
	return added, removed
}

// GenerateUnifiedDiff computes a hunked unified diff between old and new content
// with context folding, line-number gutters and ANSI coloring.
func GenerateUnifiedDiff(fileName, oldText, newText string) string {
	return GenerateUnifiedDiffWithOptions(fileName, oldText, newText, defaultDiffOptions())
}

// GenerateUnifiedDiffWithOptions is GenerateUnifiedDiff with explicit tuning.
func GenerateUnifiedDiffWithOptions(fileName, oldText, newText string, opts DiffOptions) string {
	return GenerateUnifiedDiffBetween(fileName, fileName, oldText, newText, opts)
}

// GenerateUnifiedDiffBetween renders a unified diff labeled with distinct old
// and new file names (used by the CLI `-diff a,b` preview).
func GenerateUnifiedDiffBetween(oldName, newName, oldText, newText string, opts DiffOptions) string {
	if opts.ContextLines <= 0 {
		opts.ContextLines = 3
	}
	if opts.MaxLineWidth <= 0 {
		opts.MaxLineWidth = 400
	}
	if opts.MaxOutputLines <= 0 {
		opts.MaxOutputLines = 2000
	}

	diffs := numberedDiffs(computeEdits(splitLines(oldText), splitLines(newText)))

	var sb strings.Builder
	added, removed := 0, 0
	for _, d := range diffs {
		if d.Type == '+' {
			added++
		} else if d.Type == '-' {
			removed++
		}
	}

	if !opts.OmitHeader {
		sb.WriteString(ansi("1;37", fmt.Sprintf("--- %s (original)", oldName)))
		sb.WriteString("\n")
		sb.WriteString(ansi("1;37", fmt.Sprintf("+++ %s (modified)", newName)))
		if added > 0 || removed > 0 {
			sb.WriteString(ansi("90", fmt.Sprintf("  (+%d/-%d)", added, removed)))
		}
		sb.WriteString("\n")
	}

	// Locate change regions and merge them when separated by <= 2*context lines.
	type region struct{ start, end int } // inclusive indices into diffs
	var regions []region
	for i, d := range diffs {
		if d.Type == ' ' {
			continue
		}
		if n := len(regions); n > 0 && i-regions[n-1].end <= 2*opts.ContextLines {
			regions[n-1].end = i
		} else {
			regions = append(regions, region{i, i})
		}
	}

	ctx := opts.ContextLines
	emitted := 0
	emit := func(line string) bool {
		if emitted >= opts.MaxOutputLines {
			return false
		}
		sb.WriteString(line)
		sb.WriteString("\n")
		emitted++
		return true
	}

	truncated := false
	lastEnd := -1
	for _, r := range regions {
		start := r.start - ctx
		if start < 0 {
			start = 0
		}
		end := r.end + ctx
		if end >= len(diffs) {
			end = len(diffs) - 1
		}

		// Collapse skips: if we jump over a large unchanged stretch, say so.
		if lastEnd >= 0 && start > lastEnd+1 {
			skipped := 0
			for i := lastEnd + 1; i < start; i++ {
				if diffs[i].Type == ' ' {
					skipped++
				}
			}
			if skipped > 0 {
				if !emit(ansi("90", fmt.Sprintf("  ⋮ ... %d unchanged line(s) ...", skipped))) {
					truncated = true
					break
				}
			}
		}

		// Hunk header.
		oldStart, newStart := 0, 0
		oldCount, newCount := 0, 0
		for i := start; i <= end; i++ {
			d := diffs[i]
			if d.OldLine > 0 && oldStart == 0 {
				oldStart = d.OldLine
			}
			if d.NewLine > 0 && newStart == 0 {
				newStart = d.NewLine
			}
			if d.Type != '+' {
				oldCount++
			}
			if d.Type != '-' {
				newCount++
			}
		}
		if oldStart == 0 {
			oldStart = 1
		}
		if newStart == 0 {
			newStart = 1
		}
		hunkHeader := fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldCount, newStart, newCount)
		if !emit(ansi("36", hunkHeader)) {
			truncated = true
			break
		}

		for i := start; i <= end; i++ {
			d := diffs[i]
			text := truncateANSI(d.Text, opts.MaxLineWidth)
			var line string
			switch d.Type {
			case '+':
				line = ansi("32", fmt.Sprintf("+%4d │ %s", d.NewLine, text))
			case '-':
				line = ansi("31", fmt.Sprintf("-%4d │ %s", d.OldLine, text))
			default:
				line = ansi("90", fmt.Sprintf(" %4d │ %s", d.NewLine, text))
			}
			if !emit(line) {
				truncated = true
				break
			}
		}
		if truncated {
			break
		}
		lastEnd = end
	}

	if truncated {
		sb.WriteString(ansi("33", fmt.Sprintf("  … diff truncated at %d lines (increase DiffOptions.MaxOutputLines to see more)", opts.MaxOutputLines)))
		sb.WriteString("\n")
	}

	return sb.String()
}
