package tui

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

const historyFileName = "history"
const defaultHistoryLimit = 500

// HistoryPath returns the agy history file path (~/.agy/history).
func HistoryPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".agy", historyFileName)
}

// LoadHistory reads the persistent input history (oldest first). Missing files
// yield an empty history rather than an error.
func LoadHistory() []string {
	path := HistoryPath()
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) > defaultHistoryLimit {
		lines = lines[len(lines)-defaultHistoryLimit:]
	}
	return lines
}

// AppendHistory appends a line to the persistent history file, deduplicating
// consecutive duplicates. Errors are ignored (history is best-effort).
func AppendHistory(line string) {
	line = strings.TrimSpace(line)
	if line == "" || line == "/exit" {
		return
	}
	path := HistoryPath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	if last := lastHistoryLine(path); last == line {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(line + "\n")
}

// lastHistoryLine returns the last non-empty line of the history file.
func lastHistoryLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var last string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if l := strings.TrimSpace(scanner.Text()); l != "" {
			last = l
		}
	}
	return last
}
