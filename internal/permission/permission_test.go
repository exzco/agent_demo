package permission

import (
	"agy/internal/types"
	"os"
	"path/filepath"
	"testing"
)

func toolCall(name, argsJSON string) types.ToolCall {
	return types.ToolCall{Function: types.FunctionCall{Name: name, Arguments: argsJSON}}
}

func TestParse(t *testing.T) {
	cases := map[string]Mode{
		"readonly": ReadOnly, "READ-ONLY": ReadOnly, "ro": ReadOnly,
		"workspace": Workspace, "workspace-write": Workspace,
		"full": Full, "danger-full-access": Full, "YOLO": Full,
		"confirm": Confirm, "ask": Confirm, "": Confirm,
	}
	for in, want := range cases {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %v (err %v), want %v", in, got, err, want)
		}
	}
	if _, err := Parse("banana"); err == nil {
		t.Error("Parse(banana) should fail")
	}
}

func TestReadOnlyRejectsMutation(t *testing.T) {
	g, err := NewGate(ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Check(toolCall("read_file", `{"AbsolutePath":"x"}`)); err != nil {
		t.Errorf("read_file must be allowed: %v", err)
	}
	for _, name := range []string{"write_to_file", "replace_file_content", "run_command", "invoke_subagent"} {
		if err := g.Check(toolCall(name, "{}")); err == nil {
			t.Errorf("%s must be rejected in readonly mode", name)
		}
	}
}

func TestWorkspaceContainment(t *testing.T) {
	g, err := NewGate(Workspace)
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(g.Workspace, "sub", "f.txt")
	if err := g.Check(toolCall("write_to_file", `{"TargetFile":"`+inside+`","CodeContent":"hi"}`)); err != nil {
		t.Errorf("write inside workspace must be allowed: %v", err)
	}
	if err := g.Check(toolCall("write_to_file", `{"TargetFile":"`+g.Workspace+`","CodeContent":"hi"}`)); err != nil {
		t.Errorf("writing the workspace root itself allowed: %v", err)
	}
	outside := filepath.Join(filepath.Dir(g.Workspace), "escape.txt")
	if err := g.Check(toolCall("write_to_file", `{"TargetFile":"`+outside+`","CodeContent":"hi"}`)); err == nil {
		t.Error("write outside workspace must be rejected")
	}
	// Relative paths and .. escape attempts.
	if err := g.Check(toolCall("write_to_file", `{"TargetFile":"../escape.txt","CodeContent":"hi"}`)); err == nil {
		t.Error("relative .. escape must be rejected")
	}
	if err := g.Check(toolCall("replace_file_content", `{"TargetFile":"./ok.txt","TargetContent":"a","ReplacementContent":"b"}`)); err != nil {
		t.Errorf("relative write inside workspace must be allowed: %v", err)
	}
}

func TestWorkspaceCommandCwdConfined(t *testing.T) {
	g, err := NewGate(Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Check(toolCall("run_command", `{"CommandLine":"ls"}`)); err != nil {
		t.Errorf("empty Cwd must be allowed: %v", err)
	}
	if err := g.Check(toolCall("run_command", `{"CommandLine":"ls","Cwd":"`+g.Workspace+`/sub"}`)); err != nil {
		t.Errorf("inner Cwd must be allowed: %v", err)
	}
	if err := g.Check(toolCall("run_command", `{"CommandLine":"ls","Cwd":"/etc"}`)); err == nil {
		t.Error("Cwd outside workspace must be rejected")
	}
}

func TestFullAndConfirmAllowAll(t *testing.T) {
	for _, m := range []Mode{Full, Confirm} {
		g, err := NewGate(m)
		if err != nil {
			t.Fatal(err)
		}
		outside := "/etc/agy_must_not_exist"
		for _, name := range []string{
			"run_command",
			"invoke_subagent",
			"write_to_file:" + `{"TargetFile":"` + outside + `","CodeContent":"x"}`,
		} {
			n, args := name, "{}"
			if i := indexByte(name, ':'); i >= 0 {
				n, args = name[:i], name[i+1:]
			}
			if err := g.Check(toolCall(n, args)); err != nil {
				t.Errorf("mode %s must allow %s: %v", m, n, err)
			}
		}
		_ = os.Getwd
	}
	_ = filepath.Join
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
