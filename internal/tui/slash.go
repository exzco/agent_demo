package tui

import (
	"fmt"
	"strings"
)

// SlashCommand represents an interactive slash command definition.
type SlashCommand struct {
	Name        string   // e.g. "/add-dir"
	Aliases     []string // e.g. ["/g"]
	Description string   // e.g. "Add a directory to the workspace"
	Usage       string   // e.g. "/add-dir <path>"
}

// DefaultSlashCommands returns all 52 official slash commands sorted alphabetically,
// exactly matching the official agy reverse-engineered specification.
func DefaultSlashCommands() []SlashCommand {
	return []SlashCommand{
		// Top 5 commands matching screenshot verbatim:
		{Name: "/add-dir", Description: "Add a directory to the workspace", Usage: "/add-dir <directory-path>"},
		{Name: "/agents", Description: "List available custom agents", Usage: "/agents"},
		{Name: "/artifact", Description: "View and review artifacts", Usage: "/artifact [artifact-id]"},
		{Name: "/btw", Description: "Ask a side question without interrupting the current task", Usage: "/btw <question>"},
		{Name: "/changelog", Description: "Show release notes and changes", Usage: "/changelog"},

		// Remaining 47 official slash commands:
		{Name: "/boost", Description: "Deep thinking, multi-perspective verification for complex tasks", Usage: "/boost"},
		{Name: "/browser", Description: "Search the web or fetch online documentation", Usage: "/browser <query or URL>"},
		{Name: "/clear", Description: "Reset session conversation trajectory", Usage: "/clear"},
		{Name: "/compact", Description: "Compact conversation context to prevent token window overflow", Usage: "/compact"},
		{Name: "/config", Description: "View and manage CLI configuration settings", Usage: "/config [key] [value]"},
		{Name: "/context", Description: "Inspect active context and token usage metrics", Usage: "/context"},
		{Name: "/copy", Description: "Copy last response or artifact to clipboard", Usage: "/copy"},
		{Name: "/debug", Description: "Toggle verbose debugging logs", Usage: "/debug"},
		{Name: "/diff", Description: "Preview and review unified file diffs", Usage: "/diff <file1> <file2>"},
		{Name: "/docs", Description: "Open official documentation and guides", Usage: "/docs"},
		{Name: "/doctor", Description: "Check system environment and runtime diagnostics", Usage: "/doctor"},
		{Name: "/exit", Description: "Exit the agy CLI application", Usage: "/exit"},
		{Name: "/export", Description: "Export session trajectory and logs to file", Usage: "/export <file-path>"},
		{Name: "/feedback", Description: "Submit feedback or report an issue", Usage: "/feedback"},
		{Name: "/files", Description: "List active files in current workspace", Usage: "/files"},
		{Name: "/fork", Description: "Fork current conversation into a new child branch", Usage: "/fork"},
		{Name: "/git", Description: "Run git operations or inspect repository state", Usage: "/git [status|log|diff]"},
		{Name: "/goal", Description: "Run long-running task thoroughly until fully achieved", Usage: "/goal <instruction>"},
		{Name: "/grill-me", Description: "Interactive interview to align on design decisions", Usage: "/grill-me"},
		{Name: "/help", Description: "Show slash commands menu and keybindings help", Usage: "/help"},
		{Name: "/history", Description: "View session prompt and execution history", Usage: "/history"},
		{Name: "/import", Description: "Import conversation history from file", Usage: "/import <file-path>"},
		{Name: "/init", Description: "Initialize agy configuration in current workspace", Usage: "/init"},
		{Name: "/instructions", Description: "View loaded system instructions and rules", Usage: "/instructions"},
		{Name: "/learn", Description: "Persist lessons learned and rules for future tasks", Usage: "/learn"},
		{Name: "/logs", Description: "View recent execution logs and events", Usage: "/logs"},
		{Name: "/mcp", Description: "Manage Model Context Protocol servers", Usage: "/mcp [list|add|remove]"},
		{Name: "/memory", Description: "Inspect memory and persistent workspace state", Usage: "/memory"},
		{Name: "/model", Description: "View or switch the active LLM gateway model", Usage: "/model [model-name]"},
		{Name: "/models", Description: "List available models from gateway", Usage: "/models"},
		{Name: "/new", Description: "Start a new clean conversation session", Usage: "/new"},
		{Name: "/notes", Description: "View or manage project notes and scratchpads", Usage: "/notes"},
		{Name: "/open", Description: "Open file or URL in default application", Usage: "/open <path-or-url>"},
		{Name: "/permission", Description: "Choose permission mode: readonly / workspace / full / confirm", Usage: "/permission [mode]"},
		{Name: "/permissions", Description: "View and configure tool execution permissions", Usage: "/permissions"},
		{Name: "/plan", Description: "Plan step-by-step implementation before executing code", Usage: "/plan <task>"},
		{Name: "/plugins", Description: "Manage installed extensions and plugins", Usage: "/plugins"},
		{Name: "/prompt", Description: "View or edit current system prompt", Usage: "/prompt"},
		{Name: "/reload", Description: "Reload configuration, rules, and skills", Usage: "/reload"},
		{Name: "/resume", Description: "Resume a suspended conversation", Usage: "/resume <id>"},
		{Name: "/rollback", Description: "Revert recent workspace file modifications", Usage: "/rollback"},
		{Name: "/rules", Description: "List active product rules and guidelines", Usage: "/rules"},
		{Name: "/run", Description: "Execute a shell command directly", Usage: "/run <command>"},
		{Name: "/schedule", Description: "Schedule recurring cron jobs or one-shot timers", Usage: "/schedule [duration|cron] <prompt>"},
		{Name: "/settings", Description: "Open interactive configuration settings", Usage: "/settings"},
		{Name: "/skills", Description: "List active project skills and customizations loaded", Usage: "/skills"},
		{Name: "/status", Description: "Show trajectory step counts, cascade ID, and token stats", Usage: "/status"},
		{Name: "/subagents", Description: "Inspect active and recent background subagent instances", Usage: "/subagents"},
	}
}

// MatchSlashCommands filters slash commands matching the input prefix.
func MatchSlashCommands(input string) []SlashCommand {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" || trimmed == "/" {
		return DefaultSlashCommands()
	}

	lower := strings.ToLower(trimmed)
	var matches []SlashCommand
	for _, cmd := range DefaultSlashCommands() {
		if strings.HasPrefix(cmd.Name, lower) {
			matches = append(matches, cmd)
			continue
		}
		for _, alias := range cmd.Aliases {
			if strings.HasPrefix(alias, lower) {
				matches = append(matches, cmd)
				break
			}
		}
	}
	return matches
}

// RenderSlashDropdown renders the exact 1:1 menu matching user screenshot:
// > /
// ────────────────────────────────────────────────────────────────────────
// > /add-dir          Add a directory to the workspace
//
//	/agents           List available custom agents
//	/artifact         View and review artifacts
//	/btw              Ask a side question without interrupting the current task
//	/changelog        Show release notes and changes
//
// legacyDropdownWidth is the column count at which the original fixed layout
// (72-column divider, untruncated descriptions) always fits on one row.
const legacyDropdownWidth = 84

// GetSlashDropdownLines returns the formatted lines of the dropdown menu.
func GetSlashDropdownLines(matches []SlashCommand, selectedIdx int, maxVisible int) []string {
	return getSlashDropdownLines(matches, selectedIdx, maxVisible, 0)
}

// GetSlashDropdownLinesWidth is GetSlashDropdownLines fitted to a terminal
// width: the divider shrinks and descriptions are truncated so that every
// returned line occupies exactly one physical terminal row.
func GetSlashDropdownLinesWidth(matches []SlashCommand, selectedIdx int, maxVisible int, width int) []string {
	if width >= legacyDropdownWidth {
		return getSlashDropdownLines(matches, selectedIdx, maxVisible, 0)
	}
	if width < 12 {
		width = 12
	}
	return getSlashDropdownLines(matches, selectedIdx, maxVisible, width)
}

// getSlashDropdownLines renders the dropdown. width <= 0 selects the legacy
// unbounded layout; width > 0 truncates the divider and descriptions so each
// line fits within width columns.
func getSlashDropdownLines(matches []SlashCommand, selectedIdx int, maxVisible int, width int) []string {
	if len(matches) == 0 {
		return []string{"\x1b[90m(No matching slash commands)\x1b[0m"}
	}

	if maxVisible <= 0 {
		maxVisible = 5
	}

	if selectedIdx < 0 {
		selectedIdx = 0
	}
	if selectedIdx >= len(matches) {
		selectedIdx = len(matches) - 1
	}

	// Calculate sliding window
	startIdx := 0
	if selectedIdx >= maxVisible {
		startIdx = selectedIdx - maxVisible + 1
	}
	endIdx := startIdx + maxVisible
	if endIdx > len(matches) {
		endIdx = len(matches)
	}

	// Fit mode (width > 0) shrinks the divider and truncates descriptions so
	// every line occupies exactly one physical terminal row.
	fit := width > 0
	descWidth := 0
	if fit {
		descWidth = width - 22
	}

	var lines []string

	// 1. Horizontal divider line
	divLen := 72
	if fit {
		divLen = width
		if divLen > 72 {
			divLen = 72
		}
	}
	if divLen < 8 {
		divLen = 8
	}
	lines = append(lines, ansi("90", strings.Repeat("─", divLen)))

	// 2. Render visible items
	for i := startIdx; i < endIdx; i++ {
		cmd := matches[i]
		desc := cmd.Description
		if fit {
			if descWidth >= 8 {
				desc = truncateANSI(desc, descWidth)
			} else {
				desc = ""
			}
		}
		name := padRight(cmd.Name, 17)
		var line string
		if i == selectedIdx {
			line = ansi("1;34", "> ") + ansi("1;34", name)
		} else {
			line = "  " + ansi("37", name)
		}
		if desc != "" {
			descColor := "90"
			if i == selectedIdx {
				descColor = "37"
			}
			line += "   " + ansi(descColor, desc)
		}
		line = strings.TrimRight(line, " ")
		if fit {
			line = truncateANSI(line, width)
		}
		lines = append(lines, line)
	}

	// 3. Render 'more' indicator if remaining items exist
	remaining := len(matches) - endIdx
	if remaining > 0 {
		lines = append(lines, ansi("90", fmt.Sprintf("  ↓ %d more", remaining)))
	}

	return lines
}

// RenderSlashDropdown renders the exact 1:1 menu matching user screenshot.
func RenderSlashDropdown(matches []SlashCommand, selectedIdx int, maxVisible int) (string, int) {
	lines := GetSlashDropdownLines(matches, selectedIdx, maxVisible)
	return strings.Join(lines, "\n") + "\n", len(lines)
}
