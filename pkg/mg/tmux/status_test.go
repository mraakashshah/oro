package tmux

import (
	"strings"
	"testing"

	"oro/pkg/mg"
	"oro/pkg/mg/data"
)

func TestStatusLineFormat(t *testing.T) {
	issues, _, err := data.LoadIssues("../testdata/sample.jsonl")
	if err != nil {
		t.Fatalf("LoadIssues: %v", err)
	}

	groups := data.GroupByParade(issues, data.DefaultBlockingTypes)
	got := statusLine(groups)

	// Verify tmux markup present
	if !strings.Contains(got, "#[fg=") {
		t.Errorf("expected tmux fg markup, got: %s", got)
	}

	// Verify all symbols present
	for _, sym := range []string{mg.FleurDeLis, mg.SymRolling, mg.SymLinedUp, mg.SymStalled, mg.SymPassed} {
		if !strings.Contains(got, sym) {
			t.Errorf("missing symbol %q in: %s", sym, got)
		}
	}

	// Verify correct counts
	for _, want := range []string{"3" + mg.SymRolling, "12" + mg.SymLinedUp, "3" + mg.SymStalled, "3" + mg.SymPassed} {
		if !strings.Contains(got, want) {
			t.Errorf("missing count %q in: %s", want, got)
		}
	}
}

func TestStatusLineEmptyGroups(t *testing.T) {
	groups := map[data.ParadeStatus][]data.Issue{
		data.ParadeRolling:      {},
		data.ParadeLinedUp:      {},
		data.ParadeStalled:      {},
		data.ParadePastTheStand: {},
	}

	got := statusLine(groups)

	// All counts should be 0
	for _, want := range []string{"0" + mg.SymRolling, "0" + mg.SymLinedUp, "0" + mg.SymStalled, "0" + mg.SymPassed} {
		if !strings.Contains(got, want) {
			t.Errorf("missing zero count %q in: %s", want, got)
		}
	}
}
