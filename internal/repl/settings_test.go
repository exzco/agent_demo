package repl

import "testing"

func TestNormalizeSettingKey(t *testing.T) {
	valid := map[string]string{
		"url":             "url",
		"URL":             "url",
		"gateway":         "url",
		"llm_gateway_url": "url",
		"apikey":          "key",
		"api-key":         "key",
		"API_KEY":         "key",
		"token":           "key",
		"model":           "model",
	}
	for in, want := range valid {
		if got := normalizeSettingKey(in); got != want {
			t.Errorf("normalizeSettingKey(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "nope", "temperature"} {
		if got := normalizeSettingKey(bad); got != "" {
			t.Errorf("normalizeSettingKey(%q) = %q, want empty", bad, got)
		}
	}
}

func TestNormalizeSettingKeyContext(t *testing.T) {
	for _, in := range []string{"context", "context-window", "context_window", "window", "ctx", "CTX"} {
		if got := normalizeSettingKey(in); got != "context" {
			t.Errorf("normalizeSettingKey(%q) = %q, want context", in, got)
		}
	}
}
