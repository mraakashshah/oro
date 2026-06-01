package ops //nolint:testpackage // tests unexported canonical helpers from the finding spine

import "testing"

func TestFindingID_StableAcrossEvidenceReorder(t *testing.T) {
	finding := Finding{
		Category: "correctness",
		Title:    "Worker loses review status",
		Evidence: []Evidence{
			{File: "pkg/worker/worker.go", LineStart: 42, LineEnd: 48, Quote: "status"},
			{File: "pkg/worker/drain.go", LineStart: 12, LineEnd: 14},
		},
	}
	reordered := finding
	reordered.Evidence = []Evidence{
		finding.Evidence[1],
		finding.Evidence[0],
	}

	if got, want := FindingID("oro-123", finding), FindingID("oro-123", reordered); got != want {
		t.Fatalf("FindingID changed after evidence reorder: got %q want %q", got, want)
	}
}

func TestFindingID_ChangesOnTitleOrCategoryOrFile(t *testing.T) {
	base := Finding{
		Category: "correctness",
		Title:    "Worker loses review status",
		Evidence: []Evidence{
			{File: "pkg/worker/worker.go", LineStart: 42, LineEnd: 48, Quote: "status"},
		},
	}
	baseID := FindingID("oro-123", base)

	cases := []struct {
		name   string
		mutate func(Finding) Finding
	}{
		{
			name: "title",
			mutate: func(f Finding) Finding {
				f.Title = "Worker drops review status"
				return f
			},
		},
		{
			name: "category",
			mutate: func(f Finding) Finding {
				f.Category = "architecture"
				return f
			},
		},
		{
			name: "file",
			mutate: func(f Finding) Finding {
				f.Evidence[0].File = "pkg/worker/drain.go"
				return f
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := tc.mutate(base)
			if got := FindingID("oro-123", changed); got == baseID {
				t.Fatalf("FindingID did not change after changing %s: %q", tc.name, got)
			}
		})
	}
}
