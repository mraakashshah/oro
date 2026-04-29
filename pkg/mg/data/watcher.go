package data

import (
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"oro/pkg/beadstore"
)

// FileChangedMsg signals that the issues file was modified on disk.
// Used by the app model to trigger a full parade rebuild.
// This is emitted by the polling watcher when a newer file modtime is detected.
type FileChangedMsg struct {
	Issues  []Issue
	LastMod time.Time
	Skipped int // Count of malformed JSONL lines skipped during load
}

// FileUnchangedMsg signals a completed watch poll without changes.
type FileUnchangedMsg struct {
	LastMod time.Time
}

// FileWatchErrorMsg signals a poll error (stat/load). The app should keep polling.
type FileWatchErrorMsg struct {
	Err error
}

const (
	watchInterval   = 1200 * time.Millisecond
	cliPollInterval = 5 * time.Second
)

// WatchFile polls a JSONL file and emits a single message (changed, unchanged, or error).
// Callers should schedule it again after handling the returned message.
func WatchFile(path string, lastMod time.Time) tea.Cmd {
	if path == "" {
		return nil
	}
	return tea.Tick(watchInterval, func(time.Time) tea.Msg {
		info, err := os.Stat(path)
		if err != nil {
			return FileWatchErrorMsg{Err: err}
		}

		modTime := info.ModTime()
		if !modTime.After(lastMod) {
			return FileUnchangedMsg{LastMod: lastMod}
		}

		issues, skipped, err := LoadIssues(path)
		if err != nil {
			return FileWatchErrorMsg{Err: err}
		}
		return FileChangedMsg{Issues: issues, LastMod: modTime, Skipped: skipped}
	})
}

// PollCLI polls the bead store on a timer and emits ActiveIssuesMsg.
// Only fetches non-closed issues to keep the poll fast (5 active vs 1150+ closed).
// The app merges the active snapshot with its cached closed issues.
func PollCLI(store beadstore.Store) tea.Cmd {
	return tea.Tick(cliPollInterval, func(time.Time) tea.Msg {
		issues, err := FetchActiveIssues(store)
		if err != nil {
			return FileWatchErrorMsg{Err: err}
		}
		return ActiveIssuesMsg{Issues: issues}
	})
}

// ActiveIssuesMsg carries only non-closed issues from a poll cycle.
// The app merges these with its cached closed issues.
type ActiveIssuesMsg struct {
	Issues []Issue
}

// ClosedIssuesMsg carries closed issues fetched lazily on first toggle.
type ClosedIssuesMsg struct {
	Issues []Issue
	Err    error
}

// FetchAllClosedCmd returns a tea.Cmd that fetches all closed issues in the
// background. Used to hydrate the full closed set after startup.
func FetchAllClosedCmd(store beadstore.Store) tea.Cmd {
	return func() tea.Msg {
		issues, err := FetchAllClosed(store)
		if err != nil {
			return ClosedIssuesMsg{Err: err}
		}
		return ClosedIssuesMsg{Issues: issues}
	}
}

// FileModTime returns the file's modification time.
func FileModTime(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}
