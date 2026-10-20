package llm

import "testing"

func TestDefaultContextWindow(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"deepseek-flash", 1000000},
		{"DeepSeek-Flash", 1000000},
		{"deepseek-flash-2026", 1000000},
		{"deepseek-v4-pro", 1000000},
		{"deepseek-v4.1-flash-free", 1000000},
		{"deepseek-chat", 128000},
		{"gpt-4o", 128000},
		{"", 128000},
	}
	for _, c := range cases {
		if got := DefaultContextWindow(c.model); got != c.want {
			t.Errorf("DefaultContextWindow(%q) = %d, want %d", c.model, got, c.want)
		}
	}
}
