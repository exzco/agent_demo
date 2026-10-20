package cli

import "strings"

// ValueOr returns fallback whenever v is blank (after trimming).
func ValueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// MaskSecret renders a secret for display, keeping a short prefix and suffix
// so users can recognize which key is configured without leaking it.
func MaskSecret(s string) string {
	if s == "" {
		return "(not set)"
	}
	r := []rune(s)
	if len(r) <= 8 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:3]) + strings.Repeat("*", len(r)-7) + string(r[len(r)-4:])
}
