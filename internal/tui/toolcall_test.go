package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderToolCallStyle(t *testing.T) {
	withoutColor(t)
	cases := []struct {
		name string
		args string
		want string
	}{
		{"view_file", `{"AbsolutePath":"src/app.py"}`, "○ Read(src/app.py)"},
		{"read_file", `{"AbsolutePath":"src/app.py","StartLine":3}`, "○ Read(src/app.py)"},
		{"write_to_file", `{"TargetFile":"src/utils.py","CodeContent":"x"}`, "○ Write(src/utils.py)"},
		{"replace_file_content", `{"TargetFile":"src/main.py","TargetContent":"missing"}`, "○ Edit(src/main.py)"},
		{"run_command", `{"CommandLine":"go test ./..."}`, "○ Bash(go test ./...)"},
		{"list_dir", `{"AbsolutePath":"pkg/server"}`, "○ ListDir(pkg/server)"},
		{"find_by_name", `{"Pattern":"*.config.json"}`, "○ Find(*.config.json)"},
		{"grep_search", `{"Query":"func HandleAuth"}`, "○ Grep(func HandleAuth)"},
		{"search_web", `{"Query":"Gin Gonic v2 release"}`, "○ WebSearch(Gin Gonic v2 release)"},
		{"read_url_content", `{"Url":"https://pkg.go.dev/x"}`, "○ WebFetch(https://pkg.go.dev/x)"},
		{"ask_question", `{"Question":"Choose database backend"}`, "○ Ask(Choose database backend)"},
		{"invoke_subagent", `{"Subagents":[{"Role":"Codebase Researcher","Prompt":"p"}]}`, "○ Agent(Codebase Researcher)"},
	}
	for _, c := range cases {
		if got := RenderToolCall(c.name, c.args, 0); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRenderToolCallLineSpan(t *testing.T) {
	withoutColor(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "main.py")
	content := "line one\nline two\nline three\nline four\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	got := RenderToolCall("replace_file_content",
		`{"TargetFile":"`+path+`","TargetContent":"line two\nline three"}`, 0)
	want := "○ Edit(" + path + ":L2-L3)"
	if got != want {
		t.Fatalf("line span: got %q, want %q", got, want)
	}

	got = RenderToolCall("replace_file_content",
		`{"TargetFile":"`+path+`","TargetContent":"not present"}`, 0)
	if strings.Contains(got, ":L") {
		t.Fatalf("unresolvable span must be omitted: %q", got)
	}
}

func TestRenderToolCallDots(t *testing.T) {
	saved := ColorEnabled()
	SetColorEnabled(true)
	defer SetColorEnabled(saved)

	pending := RenderToolCall("run_command", `{"CommandLine":"pwd"}`, 0)
	if pending != "\x1b[37m○\x1b[0m \x1b[33mBash\x1b[0m(pwd)" {
		t.Fatalf("pending row: got %q", pending)
	}

	okRow := RenderToolCallResult("run_command", `{"CommandLine":"pwd"}`, true, 0)
	if okRow != "\x1b[32m●\x1b[0m \x1b[33mBash\x1b[0m(pwd)" {
		t.Fatalf("success row: got %q", okRow)
	}

	failRow := RenderToolCallResult("run_command", `{"CommandLine":"pwd"}`, false, 0)
	if failRow != "\x1b[31m●\x1b[0m \x1b[33mBash\x1b[0m(pwd)" {
		t.Fatalf("failure row: got %q", failRow)
	}

	unknown := RenderToolCall("custom_tool", `{"Path":"y"}`, 0)
	if unknown != "\x1b[37m○\x1b[0m \x1b[33mcustom_tool\x1b[0m(y)" {
		t.Fatalf("unknown tool fallback: got %q", unknown)
	}
}

func TestSettleToolCall(t *testing.T) {
	withoutColor(t)
	pending := RenderToolCall("replace_file_content", `{"TargetFile":"x.py"}`, 0)
	if got := SettleToolCall(pending, true); got != "● Edit(x.py)" {
		t.Fatalf("plain settle: %q", got)
	}

	saved := ColorEnabled()
	SetColorEnabled(true)
	defer SetColorEnabled(saved)

	pending = RenderToolCall("run_command", `{"CommandLine":"pwd"}`, 0)
	if got := SettleToolCall(pending, true); got != "\x1b[32m●\x1b[0m \x1b[33mBash\x1b[0m(pwd)" {
		t.Fatalf("success settle: %q", got)
	}
	if got := SettleToolCall(pending, false); got != "\x1b[31m●\x1b[0m \x1b[33mBash\x1b[0m(pwd)" {
		t.Fatalf("failure settle: %q", got)
	}
}

func TestRenderToolCallSanitizesAndTruncates(t *testing.T) {
	withoutColor(t)

	got := RenderToolCall("run_command", `{"CommandLine":"echo a\nrm -rf b\u0007"}`, 0)
	if strings.ContainsAny(got, "\n\a") {
		t.Fatalf("control chars must be stripped: %q", got)
	}
	if !strings.Contains(got, "echo a rm -rf b") {
		t.Fatalf("newline must collapse to space: %q", got)
	}

	long := strings.Repeat("x", 200)
	got = RenderToolCall("run_command", `{"CommandLine":"`+long+`"}`, 40)
	if w := displayWidthANSI(got); w > 39 {
		t.Fatalf("truncated row too wide: %d cols", w)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix: %q", got)
	}

	empty := RenderToolCall("run_command", `{}`, 0)
	if empty != "○ Bash()" {
		t.Fatalf("empty args: %q", empty)
	}
}

func TestRenderToolOutputCollapsed(t *testing.T) {
	withoutColor(t)

	rows := RenderToolOutputCollapsed("a\nb\nc", nil, 80)
	if len(rows) != 1 || rows[0] != "  … 3 lines hidden · Ctrl+O to expand" {
		t.Fatalf("multi-line hidden hint: %q", rows)
	}
	rows = RenderToolOutputCollapsed("solo", nil, 80)
	if len(rows) != 1 || rows[0] != "  … output hidden · Ctrl+O to expand" {
		t.Fatalf("single-line hidden hint: %q", rows)
	}
	if rows := RenderToolOutputCollapsed("", nil, 80); rows != nil {
		t.Fatalf("empty result must render nothing: %q", rows)
	}

	rows = RenderToolOutputCollapsed("", errors.New("boom\nsecond"), 80)
	if len(rows) != 2 {
		t.Fatalf("error summary rows: %q", rows)
	}
	if !strings.Contains(rows[0], "Error: boom") || !strings.Contains(rows[1], "1 more line(s)") {
		t.Fatalf("error summary content: %q", rows)
	}
}

func TestRenderToolOutputExpanded(t *testing.T) {
	withoutColor(t)

	rows := RenderToolOutputExpanded("one\ntwo", errors.New("bad"), 80)
	if len(rows) != 3 || rows[0] != "  bad" || rows[1] != "  one" || rows[2] != "  two" {
		t.Fatalf("expanded rows: %q", rows)
	}

	big := strings.Repeat("line\n", maxExpandedToolOutputLines+5)
	rows = RenderToolOutputExpanded(big, nil, 80)
	if len(rows) != maxExpandedToolOutputLines+1 {
		t.Fatalf("expanded cap: got %d rows", len(rows))
	}
	if !strings.Contains(rows[len(rows)-1], "not shown") {
		t.Fatalf("missing truncation notice: %q", rows[len(rows)-1])
	}
}
