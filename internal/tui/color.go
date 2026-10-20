package tui

import (
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// colorEnabled is the global ANSI color switch. It defaults to true so that
// library consumers (and tests) get deterministic colored output unless they
// explicitly opt out. main() calls ConfigureColorFor(os.Stdout) at boot so the
// interactive experience degrades gracefully on dumb / non-TTY terminals.
var colorEnabled = true

// SetColorEnabled globally toggles ANSI color output.
func SetColorEnabled(v bool) { colorEnabled = v }

// ColorEnabled reports whether ANSI color output is currently enabled.
func ColorEnabled() bool { return colorEnabled }

// ConfigureColorFor auto-detects terminal color capability for writer w,
// honoring the NO_COLOR (https://no-color.org) and TERM=dumb conventions.
func ConfigureColorFor(w io.Writer) {
	SetColorEnabled(ShouldUseColor(w))
}

// ShouldUseColor reports whether ANSI colors should be emitted for writer w.
func ShouldUseColor(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return false
	}
	if f, ok := w.(*os.File); ok {
		return isTerminal(f.Fd())
	}
	return false
}

// ansi wraps s with the given SGR parameters when color output is enabled.
// Empty code or empty string pass through untouched.
func ansi(code, s string) string {
	if !colorEnabled || code == "" || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

const ansiReset = "\x1b[0m"

// stripANSI removes all CSI escape sequences (e.g. "\x1b[31m") from s.
func stripANSI(s string) string {
	if !strings.ContainsRune(s, '\x1b') {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
					j++
				}
				if j < len(s) {
					j++
				}
			}
			i = j
			continue
		}
		sb.WriteByte(s[i])
		i++
	}
	return sb.String()
}

// displayWidthANSI computes the visual column width of s after stripping ANSI.
func displayWidthANSI(s string) int {
	return stringDisplayWidth(stripANSI(s))
}

// DisplayWidth returns the visual column width of s, ignoring ANSI escapes.
func DisplayWidth(s string) int {
	return displayWidthANSI(s)
}

// truncateANSI truncates s to at most maxWidth visual columns while keeping
// ANSI escape sequences intact and appending an ellipsis when truncated.
func truncateANSI(s string, maxWidth int) string {
	if maxWidth <= 0 || displayWidthANSI(s) <= maxWidth {
		return s
	}
	var sb strings.Builder
	width := 0
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
					j++
				}
				if j < len(s) {
					j++
				}
			}
			sb.WriteString(s[i:j])
			i = j
			continue
		}
		r, size := decodeRune(s[i:])
		w := runeDisplayWidth(r)
		if width+w > maxWidth-1 {
			sb.WriteString("…")
			if colorEnabled {
				sb.WriteString(ansiReset)
			}
			return sb.String()
		}
		sb.WriteString(s[i : i+size])
		width += w
		i += size
	}
	return sb.String()
}

// sliceByColumns returns the substring of s covering visual columns
// [start, start+width). ANSI escape sequences are preserved and count as zero
// width; runes straddling a boundary are omitted.
func sliceByColumns(s string, start, width int) string {
	if width <= 0 || start < 0 {
		return ""
	}
	var sb strings.Builder
	col := 0
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
					j++
				}
				if j < len(s) {
					j++
				}
			}
			if col >= start && col < start+width {
				sb.WriteString(s[i:j])
			}
			i = j
			continue
		}
		r, size := decodeRune(s[i:])
		w := runeDisplayWidth(r)
		if col >= start && col+w <= start+width {
			sb.WriteString(s[i : i+size])
		}
		col += w
		i += size
	}
	return sb.String()
}

// padRight pads s with spaces to the given visual width (ANSI-aware).
func padRight(s string, width int) string {
	w := displayWidthANSI(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// decodeRune decodes the first UTF-8 rune of s.
func decodeRune(s string) (rune, int) {
	return utf8.DecodeRuneInString(s)
}
