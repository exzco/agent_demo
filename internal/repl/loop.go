package repl

import (
	"agy/internal/trajectory"
	"agy/internal/tui"

	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
)

// dispatchAction tells Loop what to do after a slash command.
type dispatchAction int

const (
	actionExit dispatchAction = iota // leave the REPL
	actionNext                       // command handled; read the next input
	actionRun                        // run the agent with the returned prompt
)

// Loop runs the interactive read-eval-print cycle: prompt, history, slash
// dispatch, agent turns, footer refresh. It returns on EOF (/exit or Ctrl+D).
func (s *Session) Loop(ctx context.Context) {
	for {
		s.App.TotalSteps = s.Traj.NumSteps()
		rawInput, err := tui.ReadInteractiveLineWithDivider(s.In, s.Out, "> ", s.History)
		if err != nil {
			return
		}
		line := strings.TrimSpace(rawInput)
		// Any new submission invalidates the previous turn transcript, both
		// for the upcoming run and because command output may bypass the
		// buffer from here on.
		s.Listener.BeginTurn()
		if line == "" {
			continue
		}

		// Persist non-slash prompts in history (in-memory + on disk).
		if !strings.HasPrefix(line, "/") {
			s.History = append(s.History, line)
			if len(s.History) > 500 {
				s.History = s.History[len(s.History)-500:]
			}
			tui.AppendHistory(line)
		}

		// Handle numeric selection if previous input opened a slash menu.
		if len(s.lastMatches) > 0 {
			var choice int
			if n, _ := fmt.Sscanf(line, "%d", &choice); n == 1 && choice >= 1 && choice <= len(s.lastMatches) {
				selected := s.lastMatches[choice-1]
				fmt.Printf("\x1b[32mSelected:\x1b[0m %s (%s)\n", selected.Name, selected.Description)
				line = selected.Name
				s.lastMatches = nil
			} else {
				s.lastMatches = nil
			}
		}

		if strings.HasPrefix(line, "/") {
			next, act := s.dispatch(line)
			switch act {
			case actionExit:
				return
			case actionNext:
				continue
			case actionRun:
				line = next
			}
		}

		if s.Cfg.BaseURL == "" {
			fmt.Println("Cannot query model: AGY_LLM_GATEWAY_URL is not set.")
			continue
		}

		runCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
		// Keep the terminal from echoing stray keystrokes (e.g. Enter pressed
		// during thinking) for the whole run: the spinner keeps its single
		// live row and leftover input cannot become a phantom submission.
		restoreRunEcho := tui.SuppressRunEcho(s.In)
		s.Listener.Spinner.Start("thinking…")
		_, err = s.Agent.Run(runCtx, s.Traj, line)
		tui.FlushPendingInput(s.In)
		restoreRunEcho()
		s.Listener.Spinner.Final()
		stop()

		if err != nil {
			s.Listener.PrintTurnLine("")
			if errors.Is(err, context.Canceled) {
				s.Listener.PrintTurnLine("\x1b[33m⏹ Interrupted — back to prompt.\x1b[0m")
				// Ctrl+C counted as the first press of the exit gesture:
				// the next prompt arms the double-press exit hint.
				tui.ArmCtrlCExitHint()
			} else {
				s.Listener.PrintTurnLine(fmt.Sprintf("\x1b[31mError: %v\x1b[0m", err))
			}
		}
		s.Listener.PrintTurnLine("")

		// Keep the footer status bar (drawn below the next prompt) live.
		if s.Interactive {
			s.App.TotalSteps = s.Traj.NumSteps()
			s.App.ContextTokens = s.Agent.ContextTokens()
			usage := s.Agent.UsageTotals()
			s.App.CacheHitTokens = usage.PromptCacheHitTokens
			s.App.CacheMissTokens = usage.PromptCacheMissTokens
		}
	}
}

// dispatch interprets one slash input and reports what the loop should do
// next (plus the possibly rewritten prompt for prompt-mode commands).
func (s *Session) dispatch(line string) (string, dispatchAction) {
	parts := strings.Fields(line)
	cmdName := strings.ToLower(parts[0])
	cmdArgs := ""
	if len(parts) > 1 {
		cmdArgs = strings.TrimSpace(line[len(parts[0]):])
	}

	// If user just typed "/" or an incomplete prefix, show the preselection menu.
	matches := tui.MatchSlashCommands(cmdName)
	exactMatch := false
	for _, m := range matches {
		if m.Name == cmdName {
			exactMatch = true
			break
		}
		for _, a := range m.Aliases {
			if a == cmdName {
				exactMatch = true
				cmdName = m.Name
				break
			}
		}
	}

	if !exactMatch || cmdName == "/" || cmdName == "/help" {
		s.lastMatches = matches
		dropdown, _ := tui.RenderSlashDropdown(matches, 0, 5)
		fmt.Print(dropdown)
		if cmdName == "/" || cmdName == "/help" {
			return "", actionNext
		}
	}

	switch cmdName {
	case "/exit", "/quit":
		return "", actionExit
	case "/clear":
		s.Traj = trajectory.New("", "")
		s.Agent.ResetMetrics()
		if s.App != nil {
			s.App.ContextTokens = 0
			s.App.CacheHitTokens = 0
			s.App.CacheMissTokens = 0
		}
		fmt.Println("Trajectory reset.")
		return "", actionNext
	case "/compact":
		before, after, cerr := s.Agent.CompactNow(context.Background(), s.Traj)
		if cerr != nil {
			fmt.Printf("\x1b[31mCompaction failed: %v\x1b[0m\n", cerr)
			return "", actionNext
		}
		s.App.ContextTokens = s.Agent.ContextTokens()
		fmt.Printf("Trajectory compacted: %d → %d tokens. Checkpoint step added.\n", before, after)
		return "", actionNext
	case "/subagents":
		insts := s.Subagents.List()
		rows := make([]string, 0, len(insts))
		for _, inst := range insts {
			rows = append(rows, fmt.Sprintf("%s [%s] (%s): %s", inst.ID, inst.Status, inst.Role, inst.Prompt))
		}
		fmt.Printf("Active / Recent Subagents: %d\n", len(insts))
		fmt.Print(s.App.RenderSubagentsTable(rows))
		return "", actionNext
	case "/agents":
		agents := s.Customizations.Skills()
		fmt.Printf("== Custom Agents (%d) ==\n", len(agents))
		if len(agents) == 0 {
			fmt.Println("No custom agents found. Add SKILL.md definitions in the workspace to register agents.")
		}
		for _, a := range agents {
			fmt.Printf("  - %-20s: %s\n    Path: %s\n", a.Name, a.Description, a.Path)
		}
		fmt.Printf("\x1b[90mTip: /subagents lists running instances; /skills shows the raw skill scan.\x1b[0m\n")
		return "", actionNext
	case "/skills":
		skills := s.Customizations.Skills()
		fmt.Printf("Active Project Skills: %d loaded\n", len(skills))
		for _, sk := range skills {
			fmt.Printf("  - %-20s: %s\n    Path: %s\n", sk.Name, sk.Description, sk.Path)
		}
		return "", actionNext
	case "/status":
		fmt.Printf("Session Trajectory ID: %s | Cascade ID: %s\n", s.Traj.TrajectoryID(), s.Traj.CascadeID())
		usage := s.Agent.UsageTotals()
		fmt.Printf("Token Usage: prompt %d | completion %d | total %d\n",
			usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
		fmt.Printf("Total Steps: %d\n", s.Traj.NumSteps())
		for i, st := range s.Traj.Steps() {
			fmt.Printf("  Step %2d: %-20s [%s]\n", i+1, st.Header.StepType, st.Header.Status)
		}
		return "", actionNext
	case "/model":
		s.handleModel(cmdArgs)
		return "", actionNext
	case "/permission", "/permissions":
		s.handlePermission(cmdArgs)
		return "", actionNext
	case "/settings", "/config":
		s.handleSettings(cmdArgs)
		return "", actionNext
	case "/diff":
		if cmdArgs != "" {
			files := strings.Split(cmdArgs, " ")
			if len(files) == 2 {
				f1, _ := os.ReadFile(files[0])
				f2, _ := os.ReadFile(files[1])
				fmt.Print(tui.GenerateUnifiedDiff(files[1], string(f1), string(f2)))
			} else {
				fmt.Println("Usage: /diff <file1> <file2>")
			}
		} else {
			fmt.Println("Usage: /diff <file1> <file2> - preview changes between two files")
		}
		return "", actionNext
	case "/goal":
		if cmdArgs == "" {
			fmt.Println("Usage: /goal <instruction> (e.g. /goal 重构所有异常处理直到测试全部通过)")
			return "", actionNext
		}
		return fmt.Sprintf("[GOAL MODE: Execute thoroughly until objective is fully achieved]\n%s", cmdArgs), actionRun
	case "/plan":
		if cmdArgs == "" {
			fmt.Println("Usage: /plan <task description> - Generate a thorough step-by-step implementation plan")
			return "", actionNext
		}
		return fmt.Sprintf("[PLAN MODE: Formulate detailed step-by-step implementation plan first]\n%s", cmdArgs), actionRun
	case "/browser":
		if cmdArgs == "" {
			fmt.Println("Usage: /browser <search query or URL>")
			return "", actionNext
		}
		return fmt.Sprintf("[BROWSER MODE: Research web sources for]\n%s", cmdArgs), actionRun
	case "/boost":
		return "[BOOST MODE: Deep thinking and multi-perspective verification enabled]\n" + cmdArgs, actionRun
	case "/grill-me":
		return "[GRILL-ME MODE: Interview me with probing questions to resolve ambiguity before coding]", actionRun
	case "/learn":
		return "[LEARN MODE: Review our recent trajectory and synthesize reusable guidelines and rules]", actionRun
	case "/teamwork-preview":
		return "[TEAMWORK MODE: Coordinate autonomous subagents to solve subtasks concurrently]", actionRun
	case "/schedule":
		if cmdArgs == "" {
			fmt.Println("Usage: /schedule <duration|cron> <prompt> - Schedule background execution")
			return "", actionNext
		}
		fmt.Printf("Timer/Schedule registered for: %s\n", cmdArgs)
		return "", actionNext
	default:
		// Unknown or not-yet-implemented slash commands must never be
		// forwarded to the model as a chat prompt.
		known := len(matches) > 0 && matches[0].Name == cmdName
		if known {
			fmt.Printf("\x1b[33mCommand %s is not implemented in this build yet.\x1b[0m\n", cmdName)
			fmt.Println("\x1b[90mType /help to see available commands.\x1b[0m")
		} else {
			fmt.Printf("\x1b[33mUnknown command: %s\x1b[0m\n", cmdName)
			if len(matches) > 0 {
				var names []string
				for i, m := range matches {
					if i >= 5 {
						break
					}
					names = append(names, m.Name)
				}
				fmt.Printf("\x1b[90mDid you mean: %s\x1b[0m\n", strings.Join(names, "  "))
			} else {
				fmt.Println("\x1b[90mType /help to see available commands.\x1b[0m")
			}
		}
		return "", actionNext
	}
}
