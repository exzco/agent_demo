package cli

import (
	"strings"
	"testing"
)

func TestMaskSecret(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "(not set)"},
		{"short", "*****"},
		{"sk-abcdef1234567889", "sk-************7889"},
	}
	for _, c := range cases {
		if got := MaskSecret(c.in); got != c.want {
			t.Errorf("MaskSecret(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMaskSecretNeverLeaksMiddle(t *testing.T) {
	key := "sk-super-secret-middle-abcdef"
	masked := MaskSecret(key)
	if strings.Contains(masked, "secret") || strings.Contains(masked, "middle") {
		t.Fatalf("masked output leaks the key: %q", masked)
	}
}

func TestValueOr(t *testing.T) {
	if got := ValueOr("", "fallback"); got != "fallback" {
		t.Fatalf("got %q", got)
	}
	if got := ValueOr("  ", "fallback"); got != "fallback" {
		t.Fatalf("got %q", got)
	}
	if got := ValueOr("x", "fallback"); got != "x" {
		t.Fatalf("got %q", got)
	}
}
