package main

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// startupLog provides step-by-step startup progress output with spinner support.
type startupLog struct {
	w     io.Writer
	isTTY bool
	mu    sync.Mutex
}

// newStartupLog creates a startup logger that writes to w.
// isTTY controls whether to use animated spinners (true) or static output (false).
func newStartupLog(w io.Writer, isTTY bool) *startupLog {
	return &startupLog{
		w:     w,
		isTTY: isTTY,
	}
}

// Step prints a completed step with a checkmark.
func (s *startupLog) Step(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.w, "✓ %s\n", msg)
}

// StepTimed prints a completed step with a checkmark and duration.
func (s *startupLog) StepTimed(msg string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.w, "✓ %s (%ds)\n", msg, int(d.Seconds()))
}

// StartSpinner starts an animated spinner for long-running operations.
// Returns a function that stops the spinner and prints the final checkmark.
// In non-TTY mode, prints a static line and the stop function just prints the checkmark.
func (s *startupLog) StartSpinner(msg string) func() {
	if !s.isTTY {
		// Non-TTY mode: print static line immediately
		s.mu.Lock()
		fmt.Fprintf(s.w, "%s\n", msg)
		s.mu.Unlock()

		// Return stop function that just prints checkmark
		return func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			fmt.Fprintf(s.w, "✓ %s\n", msg)
		}
	}

	// TTY mode: animate spinner
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)

	spinnerFrames := []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}
	frameIdx := 0

	go func() {
		defer wg.Done()
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.mu.Lock()
				fmt.Fprintf(s.w, "\r%c %s", spinnerFrames[frameIdx], msg)
				s.mu.Unlock()
				frameIdx = (frameIdx + 1) % len(spinnerFrames)
			}
		}
	}()

	// Return stop function
	stopOnce := sync.Once{}
	return func() {
		stopOnce.Do(func() {
			cancel()
			wg.Wait()

			s.mu.Lock()
			defer s.mu.Unlock()
			// Clear spinner line and print final checkmark
			fmt.Fprintf(s.w, "\r✓ %s\n", msg)
		})
	}
}
