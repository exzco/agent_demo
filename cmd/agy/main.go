package main

import (
	"agy/internal/agent"
	"agy/internal/cli"
	"agy/internal/customizations"
	"agy/internal/llm"
	"agy/internal/permission"
	"agy/internal/repl"
	"agy/internal/settings"
	"agy/internal/subagent"
	"agy/internal/tools"
	"agy/internal/trajectory"
	"agy/internal/tui"

	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

// main is the composition root only: flags, early-exit panels, dependency
// wiring and mode selection. All feature logic lives in internal/cli
// (transcript rendering) and internal/repl (sessions and slash dispatch).
func main() {
	promptFlag := flag.String("p", "", "Prompt to execute")
	modelFlag := flag.String("model", "", "Model name (overrides AGY_LLM_GATEWAY_MODEL)")
	maxStepsFlag := flag.Int("max-steps", 20, "Steps per work segment before an automatic context compaction (0 disables)")
	modelsFlag := flag.Bool("models", false, "List available gateway models")
	skillsFlag := flag.Bool("skills", false, "List discovered project skills")
	diffFlag := flag.String("diff", "", "Show diff preview between two files: file1,file2")
	yoloFlag := flag.Bool("yolo", false, "Auto-approve all mutating tool calls (no confirmation prompts)")
	versionFlag := flag.Bool("version", false, "Print version")
	flag.Parse()

	tui.ConfigureColorFor(os.Stdout)

	if *versionFlag {
		fmt.Println("agy (ACode CLI) - Recovered Edition v2.0 (with Subagent & TUI)")
		return
	}

	if *diffFlag != "" {
		parts := strings.Split(*diffFlag, ",")
		if len(parts) == 2 {
			f1, _ := os.ReadFile(parts[0])
			f2, _ := os.ReadFile(parts[1])
			fmt.Print(tui.GenerateUnifiedDiffBetween(parts[0], parts[1], string(f1), string(f2), tui.DiffOptions{}))
		} else {
			fmt.Println("Usage: -diff file1,file2")
		}
		return
	}

	// Resolve the LLM config and the context budget: env wins over settings,
	// explicit flags win over both. Unknown models default to a 128K window;
	// DeepSeek V4 models serve 1M.
	cfg := llm.ConfigFromEnv()
	maxSteps := *maxStepsFlag
	maxStepsExplicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "max-steps" {
			maxStepsExplicit = true
		}
	})
	contextWindow := 0
	contextExplicit := false
	autoCompactLimit := 0
	if st, err := settings.Load(); err == nil {
		cfg = cfg.WithFallback(llm.Config{
			BaseURL: st.GatewayURL,
			APIKey:  st.APIKey,
			Model:   st.Model,
		})
		if !maxStepsExplicit && st.MaxSteps > 0 {
			maxSteps = st.MaxSteps
		}
		if st.ContextWindow > 0 {
			contextWindow = st.ContextWindow
			contextExplicit = true
		}
		autoCompactLimit = st.AutoCompactTokenLimit
	}
	if *modelFlag != "" {
		cfg.Model = *modelFlag
	}
	if contextWindow <= 0 {
		contextWindow = llm.DefaultContextWindow(cfg.Model)
	}
	autoCompactExplicit := autoCompactLimit > 0
	if autoCompactLimit <= 0 {
		// Compact before the gateway's context window is exhausted,
		// leaving headroom for the summarization call and the response.
		autoCompactLimit = contextWindow * 4 / 5
	}

	// Sandbox gate (mirrors Codex sandbox tiers). Persisted permission mode
	// wins; empty settings keep the legacy confirm-each-mutation flow.
	permissionMode := permission.Confirm
	var permErr error
	if st, serr := settings.Load(); serr == nil && strings.TrimSpace(st.Permission) != "" {
		permissionMode, permErr = permission.Parse(st.Permission)
		if permErr != nil {
			fmt.Printf("\x1b[33m⚠ Invalid permission_mode %q in settings; using confirm.\x1b[0m\n", st.Permission)
			permissionMode = permission.Confirm
		}
	}
	gate, gerr := permission.NewGate(permissionMode)
	if gerr != nil {
		fmt.Printf("\x1b[33m⚠ Failed to resolve workspace root for permissions: %v\x1b[0m\n", gerr)
		gate = &permission.Gate{Mode: permissionMode}
	}

	// Discover Customizations and Skills (E3).
	custMgr := customizations.NewCustomizationsManager()
	_ = custMgr.LoadFromWorkspace(".")

	if *skillsFlag {
		fmt.Println("== Discovered Project Skills ==")
		skills := custMgr.Skills()
		if len(skills) == 0 {
			fmt.Println("No SKILL.md found in workspace.")
		}
		for _, s := range skills {
			fmt.Printf("  - %-20s: %s\n    Path: %s\n", s.Name, s.Description, s.Path)
		}
		return
	}

	if *modelsFlag {
		fmt.Println("== Gateway Models ==")
		fmt.Printf("Gateway URL: %s\n", cfg.BaseURL)
		fmt.Printf("WireProtocol: %s\n", cfg.WireProtocol)
		if len(cfg.Models) > 0 {
			fmt.Println("Declared Models:")
			for _, m := range cfg.Models {
				mark := " "
				if m == cfg.Model {
					mark = "*"
				}
				fmt.Printf("  %s %s\n", mark, m)
			}
		} else {
			fmt.Printf("Default Model: %s\n", cfg.Model)
		}
		return
	}

	client := llm.NewClient(cfg, nil)
	registry := tools.NewRegistry()

	// Register Subagent Manager (E1).
	subagentMgr := subagent.NewManager(client, registry)
	subagentMgr.AutoCompactTokenLimit = autoCompactLimit
	subagentMgr.Permission = gate
	registry.Register(subagent.NewInvokeSubagentTool(subagentMgr))

	// Compose system prompt with skills augmentation.
	basePrompt := agent.DefaultSystemPrompt
	if skillAug := custMgr.GeneratePromptAugmentation(); skillAug != "" {
		basePrompt += skillAug
	}
	subagentMgr.BaseSystemPrompt = basePrompt

	newAgent := func(l *cli.Listener) *agent.Agent {
		return agent.NewAgent(agent.AgentConfig{
			SystemPrompt:          basePrompt,
			MaxSteps:              maxSteps,
			AutoCompactTokenLimit: autoCompactLimit,
			Permission:            gate,
		}, client, registry, l)
	}

	sess := &repl.Session{
		In:                  os.Stdin,
		Out:                 os.Stdout,
		Client:              client,
		Cfg:                 cfg,
		Traj:                trajectory.New("", ""),
		Subagents:           subagentMgr,
		Customizations:      custMgr,
		MaxSteps:            maxSteps,
		ContextWindow:       contextWindow,
		ContextExplicit:     contextExplicit,
		AutoCompactLimit:    autoCompactLimit,
		AutoCompactExplicit: autoCompactExplicit,
		Permission:          gate,
	}

	// One-shot prompt mode (-p): non-interactive, auto-approved.
	if *promptFlag != "" {
		sess.Listener = &cli.Listener{
			Spinner: tui.NewSpinner(os.Stdout),
			Out:     os.Stdout,
			In:      os.Stdin,
		}
		sess.Agent = newAgent(sess.Listener)
		err := sess.Once(context.Background(), *promptFlag)
		if err != nil {
			if errors.Is(err, repl.ErrNoGateway) {
				fmt.Fprintf(os.Stderr, "Error: AGY_LLM_GATEWAY_URL is not set.\n")
				fmt.Fprintf(os.Stderr, "Example: export AGY_LLM_GATEWAY_URL=https://api.deepseek.com\n")
				os.Exit(1)
			}
			if errors.Is(err, context.Canceled) {
				fmt.Fprintln(os.Stderr, "\n⏹ Interrupted.")
				os.Exit(130)
			}
			fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Interactive TUI mode.
	if cfg.BaseURL == "" {
		fmt.Println("\x1b[90mNote: AGY_LLM_GATEWAY_URL not set. Running in offline/inspection mode.\x1b[0m")
	}
	tuiApp := tui.NewApp(cfg.Model, cfg.BaseURL)
	tuiApp.ContextLimit = contextWindow
	tuiApp.PermissionMode = gate.Mode.String()
	sess.App = tuiApp
	sess.Interactive = tui.ShouldUseColor(os.Stdout)
	// Confirm is the only mode with the per-mutation approval dialog;
	// sandbox modes act without asking.
	sess.Listener = &cli.Listener{
		App:          tuiApp,
		Spinner:      tui.NewSpinner(os.Stdout),
		RequireAsked: gate.Mode == permission.Confirm && !*yoloFlag,
		Out:          os.Stdout,
		In:           os.Stdin,
	}
	tui.SetDetailToggle(sess.Listener.ToggleDetails)
	footerFn := func() []string {
		if sb := tuiApp.RenderStatusBar(); sb != "" {
			return []string{sb}
		}
		return nil
	}
	tui.SetPromptFooter(footerFn)
	sess.Listener.Spinner.SetFooter(footerFn)
	sess.Agent = newAgent(sess.Listener)
	sess.History = tui.LoadHistory()

	sess.Loop(context.Background())
}
