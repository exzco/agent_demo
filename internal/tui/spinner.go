package tui

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Spinner renders a lightweight activity indicator on a single terminal line
// while the agent is waiting for the model. It optionally renders footer rows
// (the divider and status bar) below it, keeping them continuously pinned.
// It degrades to a no-op when the output is not an interactive terminal.
type Spinner struct {
	out            io.Writer
	mu             sync.Mutex
	stop           chan struct{}
	done           chan struct{}
	running        bool
	label          string
	start          time.Time
	lastFrame      string
	frozen         string
	footerFn       func() []string
	lastFooterRows int
	forceTTY       bool
}

// NewSpinner returns a spinner bound to out.
func NewSpinner(out io.Writer) *Spinner {
	return &Spinner{out: out}
}

// SetFooter sets a dynamic function that provides lines to display below the spinner.
func (s *Spinner) SetFooter(fn func() []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.footerFn = fn
}

// Footer returns the currently configured footer function.
func (s *Spinner) Footer() func() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.footerFn
}

func (s *Spinner) enabled() bool {
	if s.forceTTY {
		return true
	}
	f, ok := s.out.(*os.File)
	if !ok {
		return false
	}
	return isTerminal(f.Fd()) && colorEnabled
}

// Start begins rendering the spinner with the given label. Safe to call
// multiple times (subsequent calls while running are ignored).
func (s *Spinner) Start(label string) {
	s.mu.Lock()
	if s.running || !s.enabled() {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.label = label
	s.start = time.Now()
	s.lastFrame = ""
	s.lastFooterRows = 0
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	stop, done := s.stop, s.done
	out := s.out
	start := s.start
	s.mu.Unlock()

	go func() {
		defer close(done)
		frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
		ticker := time.NewTicker(90 * time.Millisecond)
		defer ticker.Stop()
		i := 0

		renderFrame := func() {
			elapsed := time.Since(start)
			frame := string(frames[i%len(frames)])
			s.mu.Lock()
			s.lastFrame = frame
			fn := s.footerFn
			prevRows := s.lastFooterRows
			s.mu.Unlock()

			var footerRows []string
			if fn != nil {
				width := 80
				if f, ok := out.(*os.File); ok {
					width = resolveTerminalWidth(f)
				}
				raw := fn()
				if len(raw) > 0 {
					footerRows = BuildFooterRows(raw, width)
				}
			}

			// 1. Draw spinner line
			fmt.Fprintf(out, "\r\x1b[2K%s %s %.1fs",
				ansi("36", frame), ansi("90", label), elapsed.Seconds())

			// 2. Draw footer rows below
			for _, row := range footerRows {
				fmt.Fprintf(out, "\n\r\x1b[2K%s", row)
			}

			// Clear any extra rows if previous frame had more rows
			if prevRows > len(footerRows) {
				for j := 0; j < prevRows-len(footerRows); j++ {
					fmt.Fprint(out, "\n\r\x1b[2K")
				}
				fmt.Fprintf(out, "\x1b[%dA", prevRows-len(footerRows))
			}

			// 3. Move cursor back up to spinner line
			if len(footerRows) > 0 {
				fmt.Fprintf(out, "\x1b[%dA\r", len(footerRows))
			}

			s.mu.Lock()
			s.lastFooterRows = len(footerRows)
			s.mu.Unlock()
			i++
		}

		// Draw first frame immediately
		renderFrame()

		for {
			select {
			case <-stop:
				s.mu.Lock()
				n := s.lastFooterRows
				s.mu.Unlock()
				fmt.Fprint(out, "\r\x1b[2K")
				for j := 0; j < n; j++ {
					fmt.Fprint(out, "\n\r\x1b[2K")
				}
				if n > 0 {
					fmt.Fprintf(out, "\x1b[%dA\r", n)
				}
				return
			case <-ticker.C:
				renderFrame()
			}
		}
	}()
}

// Stop halts the spinner and clears its line and any footer rows below it.
// Blocks until fully stopped so callers can safely print on the same line afterwards.
func (s *Spinner) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	stop, done := s.stop, s.done
	s.mu.Unlock()

	close(stop)
	<-done
}

// Final halts the spinner and leaves a final "elapsed time" line on screen so
// the thinking duration stays visible, keeping the last spinner glyph as the
// reasoning icon. Any footer rows below the spinner are erased.
// Returns true when a line was printed (i.e. the spinner was
// active on an interactive terminal); callers that used to rely on Stop's
// cleared line should print their own newline when false.
func (s *Spinner) Final() bool {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return false
	}
	s.running = false
	stop, done := s.stop, s.done
	out := s.out
	label := s.label
	start := s.start
	frame := s.lastFrame
	s.mu.Unlock()

	close(stop)
	<-done
	if frame == "" {
		frame = "⠋"
	}
	line := fmt.Sprintf("%s %s %.1fs",
		ansi("36", frame), ansi("90", label), time.Since(start).Seconds())
	s.mu.Lock()
	s.frozen = line
	s.mu.Unlock()
	fmt.Fprintf(out, "\r\x1b[2K%s\n", line)
	return true
}

// FrozenLine returns the line printed by the last Final call, or "" when the
// spinner never rendered one. Callers use it to record the frozen row in
// transcripts that may be repainted later.
func (s *Spinner) FrozenLine() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frozen
}
