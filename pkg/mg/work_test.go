package mg

import (
	"os"
	"testing"
)

func TestWorkCommand(t *testing.T) {
	beadID := "oro-abc1"
	projectDir := "/tmp/test-project"

	cmd := WorkCommand(beadID, projectDir)

	if cmd.Dir != projectDir {
		t.Errorf("expected Dir=%q, got %q", projectDir, cmd.Dir)
	}

	// exec.Command stores the full path or binary name in Args[0].
	// We check that the args contain "oro", "work", and the beadID.
	if len(cmd.Args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(cmd.Args), cmd.Args)
	}
	if cmd.Args[0] != "oro" {
		t.Errorf("expected Args[0]=%q, got %q", "oro", cmd.Args[0])
	}
	if cmd.Args[1] != "work" {
		t.Errorf("expected Args[1]=%q, got %q", "work", cmd.Args[1])
	}
	if cmd.Args[2] != beadID {
		t.Errorf("expected Args[2]=%q, got %q", beadID, cmd.Args[2])
	}
}

func TestParseWorkerPanesEmpty(t *testing.T) {
	panes := parseWorkerPanes("")
	if len(panes) != 0 {
		t.Errorf("expected 0 panes from empty input, got %d: %v", len(panes), panes)
	}
}

func TestParseWorkerPanesSkipsEmptyTags(t *testing.T) {
	// Lines without a tag (empty first field) should be skipped.
	output := "\t%0\n\t%1\n\t%2\n"
	panes := parseWorkerPanes(output)
	if len(panes) != 0 {
		t.Errorf("expected 0 panes, got %d: %v", len(panes), panes)
	}
}

func TestParseWorkerPanesSingleBead(t *testing.T) {
	output := "oro-abc1\t%5\n\t%0\n"
	panes := parseWorkerPanes(output)

	if len(panes) != 1 {
		t.Fatalf("expected 1 pane, got %d: %v", len(panes), panes)
	}
	if panes["oro-abc1"] != "%5" {
		t.Errorf("expected paneID=%%5 for oro-abc1, got %q", panes["oro-abc1"])
	}
}

func TestParseWorkerPanesMultipleBeads(t *testing.T) {
	output := "oro-abc1\t%5\n\t%0\noro-def2\t%8\n\t%1\noro-ghi3\t%12\n"
	panes := parseWorkerPanes(output)

	if len(panes) != 3 {
		t.Fatalf("expected 3 panes, got %d: %v", len(panes), panes)
	}
	expected := map[string]string{
		"oro-abc1": "%5",
		"oro-def2": "%8",
		"oro-ghi3": "%12",
	}
	for beadID, wantPane := range expected {
		if panes[beadID] != wantPane {
			t.Errorf("panes[%q] = %q, want %q", beadID, panes[beadID], wantPane)
		}
	}
}

func TestInTmux(t *testing.T) {
	orig := os.Getenv("TMUX")
	defer os.Setenv("TMUX", orig)

	os.Setenv("TMUX", "/tmp/tmux-1000/default,12345,0")
	if !InTmux() {
		t.Error("expected InTmux()=true when TMUX is set")
	}

	os.Unsetenv("TMUX")
	if InTmux() {
		t.Error("expected InTmux()=false when TMUX is unset")
	}
}

func TestTmuxAvailable(t *testing.T) {
	// Just verify it returns a bool without panicking.
	_ = TmuxAvailable()
}

func TestWorkAvailable(t *testing.T) {
	// Just verify it returns a bool without panicking.
	_ = WorkAvailable()
}
