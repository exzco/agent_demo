package tui

import (
	"fmt"
	"strings"
)

type UIState int

const (
	StateNormal UIState = iota
	StateDiffReview
	StateSubagents
)

type App struct {
	ActiveModel     string
	GatewayURL      string
	ContextTokens   int
	ContextLimit    int
	TotalSteps      int
	PermissionMode  string // readonly / workspace / full / confirm ("" = hidden)
	State           UIState
	ActiveDiff      string
	CacheHitTokens  int
	CacheMissTokens int
}

func NewApp(model, gateway string) *App {
	return &App{
		ActiveModel: model,
		GatewayURL:  gateway,
		State:       StateNormal,
	}
}

// RenderStatusBar renders the status bar without any background shadow.
func (a *App) RenderStatusBar() string {
	sep := ansi("90", "│")
	gw := a.GatewayURL
	if gw == "" {
		gw = "Offline"
	}
	bar := "ACode 1.0"
	if a.PermissionMode != "" {
		bar += fmt.Sprintf(" %s %s", sep, ansi("35", "Perm: "+a.PermissionMode))
	}
	bar += fmt.Sprintf(" %s %s %s Model: %s %s Gateway: %s",
		sep, a.renderContextUsage(), sep, ansi("36", a.ActiveModel), sep, ansi("33", gw))
	return bar
}

// renderContextUsage renders the "Context: 12,345 tokens 42%" segment.
// The percentage is measured against the configured context window and is
// colored green / yellow / red as the conversation approaches the limit.
// If prompt cache hits were recorded by the gateway, the percentage cached is
// displayed alongside.
func (a *App) renderContextUsage() string {
	label := fmt.Sprintf("Context: %s tokens", formatThousands(a.ContextTokens))
	if a.ContextLimit <= 0 {
		return ansi("90", label)
	}
	pct := float64(a.ContextTokens) * 100 / float64(a.ContextLimit)
	color := "32"
	if pct >= 85 {
		color = "1;31"
	} else if pct >= 60 {
		color = "33"
	}
	var pctText string
	if pct > 0 && pct < 1 {
		pctText = fmt.Sprintf("%.1f%%", pct)
	} else {
		pctText = fmt.Sprintf("%d%%", int(pct))
	}
	out := fmt.Sprintf("%s %s", label, pctText)
	if a.CacheHitTokens > 0 {
		totalMeasured := a.CacheHitTokens + a.CacheMissTokens
		if totalMeasured > 0 {
			hitPct := float64(a.CacheHitTokens) * 100 / float64(totalMeasured)
			out += fmt.Sprintf(" (%.0f%% cached)", hitPct)
		}
	}
	return ansi(color, out)
}

// BuildFooterRows formats footer lines with a divider rule above and right-alignment.
func BuildFooterRows(footerLines []string, width int) []string {
	if width < 8 {
		width = 8
	}
	var rows []string
	rows = append(rows, ansi("90", strings.Repeat("─", width)))
	for _, ln := range footerLines {
		ln = truncateANSI(ln, width)
		if pad := width - displayWidthANSI(ln); pad > 0 {
			ln = strings.Repeat(" ", pad) + ln
		}
		rows = append(rows, ln)
	}
	return rows
}

// formatThousands renders 12345 as 12,345 for readability.
func formatThousands(n int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// RenderKeybindingHints returns a compact one-line hint strip.
func (a *App) RenderKeybindingHints() string {
	hints := []string{
		"Enter send",
		"↑/↓ history",
		"Tab complete",
		"Ctrl+O details",
		"Ctrl+U/K/W edit",
		"Ctrl+C cancel",
		"Ctrl+D exit",
	}
	return ansi("90", strings.Join(hints, " · "))
}

// RenderDiffModal displays the diff review box, folding to a bounded preview.
func (a *App) RenderDiffModal(fileName, oldContent, newContent string) string {
	const innerWidth = 63
	const maxBodyLines = 40

	opts := DiffOptions{
		ContextLines:   3,
		MaxLineWidth:   innerWidth - 12,
		MaxOutputLines: maxBodyLines,
		OmitHeader:     true,
	}
	diff := GenerateUnifiedDiffWithOptions(fileName, oldContent, newContent, opts)
	added, removed := DiffStats(oldContent, newContent)

	edge := ansi("1;33", "┃")
	title := fmt.Sprintf("Reviewing Diff: %s (+%d/-%d)", fileName, added, removed)

	var sb strings.Builder
	sb.WriteString(ansi("1;33", "┏"+strings.Repeat("━", innerWidth+2)+"┓") + "\n")
	sb.WriteString(edge + " " + padRight(truncateANSI(title, innerWidth), innerWidth) + " " + edge + "\n")
	sb.WriteString(ansi("1;33", "┣"+strings.Repeat("━", innerWidth+2)+"┫") + "\n")

	bodyLines := strings.Split(strings.TrimRight(diff, "\n"), "\n")
	if len(bodyLines) == 1 && bodyLines[0] == "" {
		bodyLines = []string{ansi("90", "  (no textual changes detected)")}
	}
	for _, line := range bodyLines {
		sb.WriteString(edge + " " + padRight(truncateANSI(line, innerWidth), innerWidth) + " " + edge + "\n")
	}
	sb.WriteString(ansi("1;33", "┗"+strings.Repeat("━", innerWidth+2)+"┛") + "\n")
	sb.WriteString(ansi("32", "[KeyApprove: Enter/y]") + "  " + ansi("31", "[KeyReject: Esc/n]") + "\n")
	return sb.String()
}

// RenderSubagentsTable renders a compact subagent status table.
func (a *App) RenderSubagentsTable(rows []string) string {
	var sb strings.Builder
	sb.WriteString(ansi("1;36", "Active / Recent Subagents") + "\n")
	if len(rows) == 0 {
		sb.WriteString(ansi("90", "  (none)") + "\n")
		return sb.String()
	}
	for _, r := range rows {
		sb.WriteString("  - " + r + "\n")
	}
	return sb.String()
}
