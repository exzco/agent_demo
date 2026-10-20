// Package repl wires the terminal session around an agent: the one-shot
// prompt runner and the interactive REPL with slash-command dispatch.
// Dependencies are injected via Session so the composition root (cmd/agy)
// stays free of feature logic.
package repl

import (
	"agy/internal/agent"
	"agy/internal/cli"
	"agy/internal/customizations"
	"agy/internal/llm"
	"agy/internal/permission"
	"agy/internal/subagent"
	"agy/internal/trajectory"
	"agy/internal/tui"

	"context"
	"errors"
	"os"
	"os/signal"
)

// ErrNoGateway reports that a model call was requested without a gateway URL.
var ErrNoGateway = errors.New("AGY_LLM_GATEWAY_URL is not set")

// Session bundles every dependency the REPL needs. The composition root
// builds one Session per process; knobs (MaxSteps, ContextWindow,
// AutoCompactLimit) stay mutable so runtime changes re-derive defaults.
type Session struct {
	In, Out        *os.File
	Client         *llm.Client
	Cfg            llm.Config
	Agent          *agent.Agent
	Traj           *trajectory.Trajectory
	Subagents      *subagent.Manager
	Customizations *customizations.CustomizationsManager
	App            *tui.App
	Listener       *cli.Listener

	Interactive bool
	History     []string

	// Budget knobs (see settings.json / startup resolution).
	// ContextExplicit / AutoCompactExplicit record user-pinned values that
	// survive model switches.
	MaxSteps            int
	ContextWindow       int
	ContextExplicit     bool
	AutoCompactLimit    int
	AutoCompactExplicit bool

	// Permission is the shared sandbox gate; /permission switches its live
	// mode and every agent (incl. subagents) sees the change immediately.
	Permission *permission.Gate

	lastMatches []tui.SlashCommand
}

// Once runs a single prompt non-interactively (-p mode): spinner up, one agent
// run, spinner down. The caller owns exit-code handling.
func (s *Session) Once(ctx context.Context, prompt string) error {
	if s.Cfg.BaseURL == "" {
		return ErrNoGateway
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	s.Listener.Spinner.Start("thinking…")
	_, err := s.Agent.Run(ctx, s.Traj, prompt)
	s.Listener.Spinner.Final()
	return err
}
