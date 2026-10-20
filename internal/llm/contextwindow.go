package llm

import "strings"

// DefaultContextWindow returns the advertised context budget for a model so
// the footer percentage is relative to the real window. DeepSeek V4 models
// (incl. deepseek-flash) serve 1M tokens; everything else falls back to 128K.
func DefaultContextWindow(model string) int {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case m == "deepseek-flash", strings.HasPrefix(m, "deepseek-flash-"), strings.Contains(m, "deepseek-v4"):
		return 1000000
	}
	return 128000
}
