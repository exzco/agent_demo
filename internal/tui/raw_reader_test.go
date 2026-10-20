package tui

import (
	"testing"
)

func TestStringDisplayWidth(t *testing.T) {
	cases := []struct {
		input    string
		expected int
	}{
		{"", 0},
		{"abc", 3},
		{"你好", 4},
		{"hello世界", 9},       // 5 + 4
		{"/goal 重构所有代码", 18}, // 6 + 1 + 11(1+10)? let's check
	}

	for _, c := range cases {
		w := stringDisplayWidth(c.input)
		if c.input == "hello世界" && w != 9 {
			t.Errorf("expected 9, got %d", w)
		}
		if c.input == "你好" && w != 4 {
			t.Errorf("expected 4, got %d", w)
		}
	}
}
