package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateFinding_RejectsHallucinations(t *testing.T) {
	repoRoot := t.TempDir()
	writeReviewFixture(t, repoRoot, "pkg/worker/worker.go", strings.Join([]string{
		"package worker",
		"",
		"func run() {",
		`	println("literal evidence")`,
		"}",
	}, "\n"))

	manifest := PromptManifest{
		Shown: map[string][][2]int{
			"pkg/worker/worker.go": {{3, 4}},
		},
	}

	valid := Finding{
		Title: "valid line range",
		Evidence: []Evidence{
			{File: "pkg/worker/worker.go", LineStart: 3, LineEnd: 4},
		},
	}
	if err := ValidateFinding(manifest, repoRoot, valid); err != nil {
		t.Fatalf("valid line-range-only finding rejected: %v", err)
	}

	cases := []struct {
		name    string
		finding Finding
	}{
		{
			name: "path not in manifest",
			finding: Finding{Evidence: []Evidence{
				{File: "pkg/worker/drain.go", LineStart: 3, LineEnd: 4},
			}},
		},
		{
			name: "line outside range",
			finding: Finding{Evidence: []Evidence{
				{File: "pkg/worker/worker.go", LineStart: 2, LineEnd: 4},
			}},
		},
		{
			name: "quote not literal",
			finding: Finding{Evidence: []Evidence{
				{File: "pkg/worker/worker.go", LineStart: 3, LineEnd: 4, Quote: "invented evidence"},
			}},
		},
		{
			name: "path escape",
			finding: Finding{Evidence: []Evidence{
				{File: "../outside.go", LineStart: 1, LineEnd: 1},
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateFinding(manifest, repoRoot, tc.finding); err == nil {
				t.Fatalf("ValidateFinding accepted hallucinated finding: %#v", tc.finding)
			}
		})
	}
}

func TestPartitionFindings(t *testing.T) {
	repoRoot := t.TempDir()
	writeReviewFixture(t, repoRoot, "pkg/ops/finding.go", strings.Join([]string{
		"package ops",
		"",
		"type Finding struct{}",
	}, "\n"))

	manifest := PromptManifest{
		Shown: map[string][][2]int{
			"pkg/ops/finding.go": {{1, 3}},
		},
	}
	valid := Finding{
		Title: "valid",
		Evidence: []Evidence{
			{File: "pkg/ops/finding.go", LineStart: 1, LineEnd: 3},
		},
	}
	invalid := Finding{
		Title: "invalid",
		Evidence: []Evidence{
			{File: "pkg/ops/other.go", LineStart: 1, LineEnd: 1},
		},
	}

	kept, dropped := PartitionFindings(manifest, repoRoot, []Finding{valid, invalid})

	if len(kept) != 1 || kept[0].Title != "valid" {
		t.Fatalf("kept findings = %#v, want only valid finding", kept)
	}
	if len(dropped) != 1 {
		t.Fatalf("dropped findings = %#v, want one invalid finding", dropped)
	}
	if dropped[0].Layer != "validation" {
		t.Fatalf("dropped layer = %q, want validation", dropped[0].Layer)
	}
	if dropped[0].Finding.Title != "invalid" {
		t.Fatalf("dropped finding = %#v, want invalid finding", dropped[0].Finding)
	}
	if dropped[0].Reason == "" {
		t.Fatal("dropped finding reason is empty")
	}
}

func writeReviewFixture(t *testing.T, repoRoot, relPath, content string) {
	t.Helper()

	path := filepath.Join(repoRoot, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("create fixture dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
