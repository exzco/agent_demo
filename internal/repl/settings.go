package repl

import (
	"agy/internal/cli"
	"agy/internal/llm"
	"agy/internal/permission"
	"agy/internal/settings"
	"agy/internal/tui"

	"fmt"
	"os"
	"strconv"
	"strings"
)

// contextSourceLabel annotates the context window with its provenance.
func contextSourceLabel(explicit bool) string {
	if explicit {
		return " (configured)"
	}
	return " (model default)"
}

// normalizeSettingKey maps user-facing setting names to canonical field keys.
func normalizeSettingKey(token string) string {
	t := strings.ToLower(strings.TrimSpace(token))
	t = strings.ReplaceAll(t, "_", "-")
	switch t {
	case "url", "gateway", "gateway-url", "llm-gateway-url", "base-url":
		return "url"
	case "key", "apikey", "api-key", "token":
		return "key"
	case "model":
		return "model"
	case "context", "context-window", "window", "ctx":
		return "context"
	}
	return ""
}

// recalibrate re-derives the context budget defaults after a model or
// explicit context-window change, unless the user pinned them.
func (s *Session) recalibrate() {
	if !s.ContextExplicit {
		s.ContextWindow = llm.DefaultContextWindow(s.Cfg.Model)
		s.App.ContextLimit = s.ContextWindow
	}
	if !s.AutoCompactExplicit {
		s.AutoCompactLimit = s.ContextWindow * 4 / 5
	}
	s.Agent.SetAutoCompactLimit(s.AutoCompactLimit)
}

// applySetting mutates the live config, hot-swaps it into the client and
// persists it to ~/.agy/settings.json.
func (s *Session) applySetting(field, value string) {
	switch field {
	case "url":
		s.Cfg.BaseURL = value
		s.App.GatewayURL = value
		fmt.Printf("\x1b[32mLLM Gateway URL updated:\x1b[0m %s\n", value)
	case "key":
		s.Cfg.APIKey = value
		fmt.Printf("\x1b[32mAPI key updated:\x1b[0m %s\n", cli.MaskSecret(value))
	case "model":
		s.Cfg.Model = value
		s.App.ActiveModel = value
		s.recalibrate()
		fmt.Printf("\x1b[32mModel updated:\x1b[0m %s (context window %d tokens)\n", value, s.ContextWindow)
	case "context":
		n, perr := strconv.Atoi(strings.ReplaceAll(strings.TrimSpace(value), ",", ""))
		if perr != nil || n <= 0 {
			fmt.Printf("\x1b[33mInvalid context window: %s (expected a positive token count)\x1b[0m\n", value)
			return
		}
		s.ContextWindow = n
		s.ContextExplicit = true
		s.App.ContextLimit = n
		if !s.AutoCompactExplicit {
			s.AutoCompactLimit = s.ContextWindow * 4 / 5
		}
		s.Agent.SetAutoCompactLimit(s.AutoCompactLimit)
		fmt.Printf("\x1b[32mContext window updated:\x1b[0m %d tokens\n", n)
	}
	s.Client.SetConfig(s.Cfg)
	s.saveSettings()
}

// permissionMode renders the live sandbox mode for persistence ("confirm" is
// persisted explicitly so a fresh boot replays the same behavior).
func (s *Session) permissionMode() string {
	if s.Permission == nil {
		return ""
	}
	return s.Permission.Mode.String()
}

// saveSettings persists the current session knobs to ~/.agy/settings.json.
func (s *Session) saveSettings() {
	err := settings.Save(settings.Settings{
		GatewayURL:            s.Cfg.BaseURL,
		APIKey:                s.Cfg.APIKey,
		Model:                 s.Cfg.Model,
		ContextWindow:         s.ContextWindow,
		MaxSteps:              s.MaxSteps,
		AutoCompactTokenLimit: s.AutoCompactLimit,
		Permission:            s.permissionMode(),
	})
	if err != nil {
		fmt.Printf("\x1b[33m⚠ Failed to save settings: %v\x1b[0m\n", err)
		return
	}
	if p := settings.Path(); p != "" {
		fmt.Printf("Saved to %s\n", p)
	}
}

// handlePermission implements /permission and /permissions: pick the sandbox
// mode interactively or set it one-shot (/permission workspace). Confirm is
// the legacy per-mutation approval flow.
func (s *Session) handlePermission(cmdArgs string) {
	var m permission.Mode
	if cmdArgs != "" {
		mode, err := permission.Parse(cmdArgs)
		if err != nil {
			fmt.Printf("\x1b[33m%s\x1b[0m\n", err)
			return
		}
		m = mode
	} else {
		modes := []permission.Mode{permission.Confirm, permission.Workspace, permission.Full, permission.ReadOnly}
		items := make([]tui.SelectItem, 0, len(modes))
		for _, cand := range modes {
			tag := ""
			if s.Permission != nil && cand == s.Permission.Mode {
				tag = "current"
			}
			items = append(items, tui.SelectItem{Key: cand.String(), Label: cand.String(), Tag: tag})
		}
		picked, PyErr := tui.RunSelect(s.In, s.Out, "Permission mode",
			items, "readonly | workspace | full | confirm · ↑/↓ move · type to filter · Enter select · esc close")
		if PyErr != nil || picked == "" {
			return
		}
		mode, err := permission.Parse(picked)
		if err != nil {
			return
		}
		m = mode
	}
	s.Permission.SetMode(m)
	s.App.PermissionMode = m.String()
	// The interactive confirm flow only lives in the confirm mode; every
	// sandbox mode above it acts without asking.
	s.Listener.RequireAsked = m == permission.Confirm
	s.saveSettings()
	fmt.Printf("\x1b[32mPermission mode set to:\x1b[0m \x1b[35m%s\x1b[0m\n", m.String())
}

// handleSettings implements /settings and /config: one-shot field updates,
// a read-only view for non-TTYs and the interactive searchable sub-menu.
func (s *Session) handleSettings(cmdArgs string) {
	showSettings := func() {
		fmt.Println("== Settings ==")
		fmt.Printf("  1. LLM Gateway URL : %s\n", cli.ValueOr(s.Cfg.BaseURL, "(not set)"))
		fmt.Printf("  2. API Key         : %s\n", cli.MaskSecret(s.Cfg.APIKey))
		fmt.Printf("  3. Model           : %s\n", cli.ValueOr(s.Cfg.Model, "(not set)"))
		fmt.Printf("  4. Context Window  : %d tokens%s\n", s.ContextWindow, contextSourceLabel(s.ContextExplicit))
		if p := settings.Path(); p != "" {
			fmt.Printf("  Config file        : %s\n", p)
		}
		gwEnv := os.Getenv("AGY_LLM_GATEWAY_URL") != "" || os.Getenv("AGY_LLM_GATEWAY_API_KEY") != ""
		if gwEnv {
			fmt.Println("  Note: AGY_LLM_GATEWAY_* env vars take precedence over saved settings on startup.")
		}
	}
	usage := "Usage: /settings url <value> | /settings key <value> | /settings model <value> | /settings context <tokens>"

	// One-shot form: /settings <field> <value>.
	if cmdArgs != "" {
		parts := strings.Fields(cmdArgs)
		field := normalizeSettingKey(parts[0])
		if field == "" {
			fmt.Println(usage)
			return
		}
		value := strings.TrimSpace(cmdArgs[len(parts[0]):])
		if value == "" {
			fmt.Println("Empty value; nothing changed.")
			return
		}
		s.applySetting(field, value)
		return
	}

	if !tui.IsTerminal(s.In) {
		showSettings()
		fmt.Println("\n" + usage)
		return
	}

	// Interactive sub-menu: a searchable list of settings; each
	// edit returns to the list until the user presses esc.
	settingsItems := func() []tui.SelectItem {
		return []tui.SelectItem{
			{Key: "url", Label: "LLM Gateway URL", Tag: cli.ValueOr(s.Cfg.BaseURL, "(not set)")},
			{Key: "key", Label: "API Key", Tag: cli.MaskSecret(s.Cfg.APIKey)},
			{Key: "model", Label: "Model", Tag: cli.ValueOr(s.Cfg.Model, "(not set)")},
			{Key: "context", Label: "Context Window", Tag: fmt.Sprintf("%d tokens", s.ContextWindow)},
		}
	}
	if p := settings.Path(); p != "" {
		fmt.Printf("\x1b[90mConfig file: %s\x1b[0m\n", p)
	}
	for {
		field, serr := tui.RunSelect(s.In, s.Out, "Settings", settingsItems(), "↑/↓ move · type to filter · Enter edit · esc close")
		if serr != nil || field == "" {
			break
		}
		var value string
		if field == "key" {
			value, _ = tui.ReadSecretLine(s.In, s.Out, "New API key: ")
		} else {
			value, _ = tui.ReadInteractiveLineWithHistory(s.In, s.Out, "New value: ", nil)
		}
		value = strings.TrimSpace(value)
		if value == "/exit" {
			// Ctrl+D / double Ctrl+C at the sub-prompt closes settings.
			break
		}
		if value == "" {
			fmt.Println("Empty value; nothing changed.")
		} else {
			s.applySetting(field, value)
		}
	}
}

// handleModel implements /model: one-shot switch, interactive picker from the
// gateway's declared model list, or a read-only summary. Switching is
// session-only; persisted changes go through /settings model.
func (s *Session) handleModel(cmdArgs string) {
	if cmdArgs != "" {
		s.Cfg.Model = cmdArgs
		s.App.ActiveModel = cmdArgs
		s.Client.SetConfig(s.Cfg)
		s.recalibrate()
		fmt.Printf("Switched active model to: \x1b[36m%s\x1b[0m (context window %d tokens)\n", s.Cfg.Model, s.ContextWindow)
		return
	}
	if len(s.Cfg.Models) > 0 && tui.IsTerminal(s.In) {
		modelItems := make([]tui.SelectItem, 0, len(s.Cfg.Models))
		for _, m := range s.Cfg.Models {
			tag := ""
			if m == s.Cfg.Model {
				tag = "current"
			}
			modelItems = append(modelItems, tui.SelectItem{Key: m, Label: m, Tag: tag})
		}
		picked, _ := tui.RunSelect(s.In, s.Out, "Select model", modelItems, "↑/↓ move · type to filter · Enter select · esc close")
		if picked != "" {
			s.Cfg.Model = picked
			s.App.ActiveModel = picked
			s.Client.SetConfig(s.Cfg)
			s.recalibrate()
			fmt.Printf("Switched active model to: \x1b[36m%s\x1b[0m (context window %d tokens)\n", picked, s.ContextWindow)
		}
		return
	}
	if len(s.Cfg.Models) > 0 {
		fmt.Println("Available models:", strings.Join(s.Cfg.Models, ", "))
	}
	fmt.Println("Current active model:", s.Cfg.Model)
	fmt.Println("Usage to switch: /model <model-name>")
}
