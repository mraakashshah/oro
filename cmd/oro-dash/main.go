package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"oro/pkg/dashboard/data"
	"oro/pkg/dashboard/views"

	"github.com/charmbracelet/x/ansi"
	"github.com/google/go-cmp/cmp"
)

const (
	headlessSnapshotWidth  = 100
	headlessSnapshotHeight = 24
)

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("oro-dash", flag.ContinueOnError)
	fs.SetOutput(stderr)

	headless := fs.Bool("headless", false, "run without an interactive TTY")
	diffTest := fs.Bool("diff-test", false, "compare the deterministic dashboard snapshot")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument: %s\n", fs.Arg(0))
		return 2
	}

	switch {
	case *headless && *diffTest:
		if err := compareDashboardSnapshot(expectedHeadlessDashboardSnapshot); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "dashboard diff-test passed")
		return 0
	case *headless || *diffTest:
		fmt.Fprintln(stderr, "--headless and --diff-test must be used together")
		return 2
	default:
		fmt.Fprintln(stderr, "interactive oro-dash is not implemented; use --headless --diff-test")
		return 2
	}
}

func compareDashboardSnapshot(expected string) error {
	got := renderHeadlessDashboardSnapshot()
	if diff := cmp.Diff(expected, got); diff != "" {
		return fmt.Errorf("dashboard snapshot mismatch (-want +got):\n%s", diff)
	}
	return nil
}

func renderHeadlessDashboardSnapshot() string {
	parade := views.NewParade(headlessSnapshotIssues(), headlessSnapshotWidth, headlessSnapshotHeight, data.DefaultBlockingTypes)
	return normalizeDashboardSnapshot(ansi.Strip(parade.View()))
}

func normalizeDashboardSnapshot(snapshot string) string {
	lines := strings.Split(snapshot, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n") + "\n"
}

func headlessSnapshotIssues() []data.Issue { //nolint:funlen // fixture data is intentionally verbose
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	return []data.Issue{
		{
			ID:        "roll-1",
			Title:     "Worker is moving",
			Status:    data.StatusInProgress,
			Priority:  data.PriorityHigh,
			IssueType: data.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
			Labels:    []string{},
			Metadata:  map[string]interface{}{},
			Tags:      []string{},
		},
		{
			ID:        "abc-1",
			Title:     "Epic parent",
			Status:    data.StatusOpen,
			Priority:  data.PriorityMedium,
			IssueType: data.TypeEpic,
			CreatedAt: now,
			UpdatedAt: now,
			Labels:    []string{},
			Metadata:  map[string]interface{}{},
			Tags:      []string{},
		},
		{
			ID:            "xyz-3",
			Title:         "Flat child with explicit parent",
			Status:        data.StatusOpen,
			Priority:      data.PriorityMedium,
			IssueType:     data.TypeTask,
			ParentIDValue: "abc-1",
			CreatedAt:     now,
			UpdatedAt:     now,
			Labels:        []string{},
			Metadata:      map[string]interface{}{},
			Tags:          []string{},
		},
		{
			ID:           "stall-1",
			Title:        "Blocked on missing input",
			Status:       data.StatusOpen,
			Priority:     data.PriorityCritical,
			IssueType:    data.TypeBug,
			Dependencies: []data.Dependency{{IssueID: "stall-1", DependsOnID: "missing-1", Type: "blocks"}},
			CreatedAt:    now,
			UpdatedAt:    now,
			Labels:       []string{},
			Metadata:     map[string]interface{}{},
			Tags:         []string{},
		},
		{
			ID:        "done-1",
			Title:     "Already done",
			Status:    data.StatusClosed,
			Priority:  data.PriorityLow,
			IssueType: data.TypeChore,
			CreatedAt: now,
			UpdatedAt: now,
			Labels:    []string{},
			Metadata:  map[string]interface{}{},
			Tags:      []string{},
		},
	}
}

const expectedHeadlessDashboardSnapshot = `╭─ ● Rolling¹ ──────────────────────────────────────────────────────────────────────────────────── ╮
│ > ● roll-1 Worker is moving P1                                                                   │
╰──────────────────────────────────────────────────────────────────────────────────────────────────╯
╭─ ♪ Lined Up² ─────────────────────────────────────────────────────────────────────────────────── ╮
│   ♪ abc-1 Epic parent P2                                                                         │
│     ♪ xyz-3 Flat child with explicit parent P2                                                   │
╰──────────────────────────────────────────────────────────────────────────────────────────────────╯
╭─ ⊘ Stalled¹ ──────────────────────────────────────────────────────────────────────────────────── ╮
│   ⊘ stall-1 Blocked on missing input P0 next → missing missing-1                                 │
╰──────────────────────────────────────────────────────────────────────────────────────────────────╯
╭─ ▶ ✓ Past the Stand¹ press c ─────────────────────────────────────────────────────────────────── ╮
╰──────────────────────────────────────────────────────────────────────────────────────────────────╯
`
