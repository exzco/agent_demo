// Package permission implements the sandbox modes that govern tool
// execution, mirroring Codex's sandbox tiers: read-only, workspace-write
// (confined to the current directory, network allowed, no confirmation) and
// danger-full-access. The legacy confirm-per-mutation flow coexists as a
// fourth session mode that stays outside this package's sandbox logic.
package permission

import (
	"agy/internal/tools"
	"agy/internal/types"

	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Mode is a sandbox permission mode.
type Mode int

const (
	// ReadOnly rejects every tool except genuinely read-only ones.
	ReadOnly Mode = iota
	// Workspace allows everything confined to the workspace root (file
	// mutations inside the root, commands rooted there, network allowed,
	// no confirmation prompts).
	Workspace
	// Full allows every tool without any sandbox restriction.
	Full
	// Confirm keeps the interactive per-mutation approval flow (legacy
	// default); sandbox-wise it behaves like Full.
	Confirm
)

// Parse maps a /permission argument to a Mode.
func Parse(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "readonly", "read-only", "ro", "read":
		return ReadOnly, nil
	case "workspace", "workspace-write", "workdir", "workspace_write":
		return Workspace, nil
	case "full", "full-access", "fullaccess", "danger-full-access", "all", "yolo":
		return Full, nil
	case "confirm", "ask", "default", "confirm-each", "":
		return Confirm, nil
	}
	return Confirm, fmt.Errorf("unknown permission mode %q (want readonly | workspace | full | confirm)", s)
}

// String renders the canonical mode name used in the UI and settings.
func (m Mode) String() string {
	switch m {
	case ReadOnly:
		return "readonly"
	case Workspace:
		return "workspace"
	case Full:
		return "full"
	}
	return "confirm"
}

// Description is the one-line picker label for each mode.
func (m Mode) Description() string {
	switch m {
	case ReadOnly:
		return "Read-only — only viewing tools run"
	case Workspace:
		return "Workspace — read/write inside current dir, network allowed, no confirmation"
	case Full:
		return "Full access — unrestricted execution, no confirmation"
	}
	return "Confirm each mutating tool call (default)"
}

// readOnlyTools are allowed in every mode.
var readOnlyTools = map[string]bool{
	"read_file": true,
}

// Gate decides whether a tool call may run under the current mode. The zero
// value behaves like Confirm; cmd/agy always ships an explicit Gate.
type Gate struct {
	Mode      Mode
	Workspace string // absolute workspace root for the Workspace mode
}

// NewGate builds a Gate rooted at the current working directory.
func NewGate(m Mode) (*Gate, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	return &Gate{Mode: m, Workspace: root}, nil
}

// SetMode switches the live mode (via /permission at runtime).
func (g *Gate) SetMode(m Mode) { g.Mode = m }

// Check reports whether the tool call is permitted. A non-nil error is fed
// back to the model as the tool result so it can adapt (e.g. stop trying or
// suggest switching modes).
func (g *Gate) Check(call types.ToolCall) error {
	if readOnlyTools[call.Function.Name] {
		return nil
	}
	switch g.Mode {
	case Full, Confirm:
		return nil
	case ReadOnly:
		return fmt.Errorf("permission denied: mode is readonly, tool %q cannot run", call.Function.Name)
	case Workspace:
		return g.checkWorkspace(call)
	}
	return nil
}

// checkWorkspace confines mutating file tools to the workspace root. Network
// is allowed on purpose (per requirement), so run_command executes freely as
// long as its declared working directory stays inside the root.
func (g *Gate) checkWorkspace(call types.ToolCall) error {
	switch call.Function.Name {
	case "write_to_file", "replace_file_content":
		var target string
		if call.Function.Name == "write_to_file" {
			var a tools.WriteFileArgs
			if err := unmarshalArgs(call, &a); err != nil {
				return err
			}
			target = a.TargetFile
		} else {
			var a tools.EditFileArgs
			if err := unmarshalArgs(call, &a); err != nil {
				return err
			}
			target = a.TargetFile
		}
		if !g.contains(target) {
			return fmt.Errorf("permission denied: %s targets %q outside the workspace (%s); switch to /permission full to allow it",
				call.Function.Name, target, g.Workspace)
		}
	case "run_command":
		var a tools.RunCommandArgs
		if err := unmarshalArgs(call, &a); err != nil {
			return err
		}
		if a.Cwd != "" && !g.contains(a.Cwd) {
			return fmt.Errorf("permission denied: run_command Cwd %q is outside the workspace (%s)", a.Cwd, g.Workspace)
		}
	}
	return nil
}

// unmarshalArgs decodes tool-call arguments into the tool's args struct.
func unmarshalArgs(call types.ToolCall, v any) error {
	if err := json.Unmarshal([]byte(call.Function.Arguments), v); err != nil {
		return fmt.Errorf("%s: invalid arguments: %w", call.Function.Name, err)
	}
	return nil
}

// contains reports whether p resolves inside the workspace root. Both sides
// are resolved through symlinks where possible, closing the classic ../
// escape for existing paths.
func (g *Gate) contains(p string) bool {
	root := filepath.Clean(g.Workspace)
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	if realRoot, rerr := filepath.EvalSymlinks(root); rerr == nil {
		root = filepath.Clean(realRoot)
	}
	if real, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		abs = filepath.Clean(real)
	} else {
		// The target may not exist yet (a write); resolve its directory.
		if realDir, derr := filepath.EvalSymlinks(filepath.Dir(abs)); derr == nil {
			abs = filepath.Join(realDir, filepath.Base(abs))
		}
	}
	if abs == root {
		return true
	}
	return strings.HasPrefix(abs, root+string(filepath.Separator))
}
