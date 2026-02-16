package main

import (
	"log"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fsnotify/fsnotify"
)

// fsChangeMsg is sent when a file change is detected in .beads/ directory.
type fsChangeMsg struct{}

// watchBeadsDir creates a file system watcher for the .beads/ directory.
// Returns nil if the directory doesn't exist or watcher creation fails
// (dashboard will fall back to polling-only mode).
func watchBeadsDir(beadsDir string) tea.Cmd {
	watcher := initWatcher(beadsDir)
	if watcher == nil {
		return nil
	}
	return runWatcher(watcher)
}

// initWatcher creates and initializes a file system watcher for the given directory.
// Returns nil if initialization fails.
func initWatcher(beadsDir string) *fsnotify.Watcher {
	// Check if .beads/ directory exists
	if _, err := os.Stat(beadsDir); err != nil {
		// Directory doesn't exist, fall back to polling
		return nil
	}

	// Create watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		// Log warning but don't error - fall back to polling
		log.Printf("fsnotify: failed to create watcher: %v (falling back to polling)", err)
		return nil
	}

	// Add .beads/ directory to watcher
	if err := watcher.Add(beadsDir); err != nil {
		_ = watcher.Close() // Best effort close
		log.Printf("fsnotify: failed to watch %s: %v (falling back to polling)", beadsDir, err)
		return nil
	}

	return watcher
}

// runWatcher returns a tea.Cmd that monitors file system events and returns fsChangeMsg
// when changes are detected (with debouncing to avoid thundering herd).
func runWatcher(watcher *fsnotify.Watcher) tea.Cmd {
	return func() tea.Msg {
		debounceTimer := newDebounceTimer()
		defer debounceTimer.Stop()

		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return nil
				}
				_ = event // Acknowledge event received
				resetDebounceTimer(debounceTimer)

			case <-debounceTimer.C:
				// Debounce period elapsed, return fsChangeMsg
				return fsChangeMsg{}

			case err, ok := <-watcher.Errors:
				if !ok {
					return nil
				}
				log.Printf("fsnotify: watcher error: %v", err)
				return nil
			}
		}
	}
}

// newDebounceTimer creates a new timer for debouncing file system events.
func newDebounceTimer() *time.Timer {
	timer := time.NewTimer(0)
	if !timer.Stop() {
		<-timer.C
	}
	return timer
}

// resetDebounceTimer resets the debounce timer to prevent rapid-fire events.
func resetDebounceTimer(timer *time.Timer) {
	const debounceDuration = 100 * time.Millisecond
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(debounceDuration)
}
