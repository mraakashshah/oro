package beadstore

import "testing"

func TestExtractAC(t *testing.T) {
	tests := []struct {
		name     string
		desc     string
		wantAC   string
		wantDesc string
	}{
		{
			name:     "extracts acceptance criteria section",
			desc:     "Some description.\n\n## Acceptance Criteria\n- [ ] First criterion\n- [ ] Second criterion",
			wantAC:   "- [ ] First criterion\n- [ ] Second criterion",
			wantDesc: "Some description.",
		},
		{
			name:     "stops at next h2 header and preserves following section",
			desc:     "Description.\n\n## Acceptance Criteria\n- [ ] Do the thing\n\n## Fix\nSome fix details.",
			wantAC:   "- [ ] Do the thing",
			wantDesc: "Description.\n\n## Fix\nSome fix details.",
		},
		{
			name:     "content before acceptance criteria",
			desc:     "## Fix\nDo X.\n\n## Acceptance Criteria\n- [ ] Widget renders\n- [ ] Tests pass",
			wantAC:   "- [ ] Widget renders\n- [ ] Tests pass",
			wantDesc: "## Fix\nDo X.",
		},
		{
			name:     "lowercase header",
			desc:     "## Context\nSome context.\n\n## Acceptance criteria\n- [ ] Works case-insensitively\n- [ ] Tests pass",
			wantAC:   "- [ ] Works case-insensitively\n- [ ] Tests pass",
			wantDesc: "## Context\nSome context.",
		},
		{
			name:     "uppercase bare header",
			desc:     "## Description\nSome description.\n\nACCEPTANCE CRITERIA\n- [ ] Uppercase works\n- [ ] No hash marks needed",
			wantAC:   "- [ ] Uppercase works\n- [ ] No hash marks needed",
			wantDesc: "## Description\nSome description.",
		},
		{
			name:     "mixed case header",
			desc:     "## Context\nContext here.\n\n## acceptance Criteria\n- [ ] Mixed case works",
			wantAC:   "- [ ] Mixed case works",
			wantDesc: "## Context\nContext here.",
		},
		{
			name:     "uppercase h2 header",
			desc:     "## ACCEPTANCE CRITERIA\n- [ ] All caps with hashes\n- [ ] Should work",
			wantAC:   "- [ ] All caps with hashes\n- [ ] Should work",
			wantDesc: "",
		},
		{
			name:     "bare header at start",
			desc:     "acceptance criteria\ncontent",
			wantAC:   "content",
			wantDesc: "",
		},
		{
			name:     "idempotent already stripped input",
			desc:     "Just a plain description with no acceptance criteria section.",
			wantAC:   "",
			wantDesc: "Just a plain description with no acceptance criteria section.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAC, gotDesc, err := extractAndStripAC(tt.desc)
			if err != nil {
				t.Fatalf("extractAndStripAC() error = %v", err)
			}
			if gotAC != tt.wantAC {
				t.Errorf("extractAndStripAC() ac:\ngot:  %q\nwant: %q", gotAC, tt.wantAC)
			}
			if gotDesc != tt.wantDesc {
				t.Errorf("extractAndStripAC() desc:\ngot:  %q\nwant: %q", gotDesc, tt.wantDesc)
			}

			secondAC, secondDesc, err := extractAndStripAC(gotDesc)
			if err != nil {
				t.Fatalf("extractAndStripAC() second pass error = %v", err)
			}
			if secondAC != "" {
				t.Errorf("extractAndStripAC() second pass ac = %q, want empty", secondAC)
			}
			if secondDesc != gotDesc {
				t.Errorf("extractAndStripAC() second pass desc = %q, want %q", secondDesc, gotDesc)
			}
		})
	}
}

func TestExtractAndStripACPublicWrapper(t *testing.T) {
	ac, desc, err := ExtractAndStripAC("Build the thing.\n\n## Acceptance Criteria\n- [ ] It works")
	if err != nil {
		t.Fatalf("ExtractAndStripAC: %v", err)
	}
	if ac != "- [ ] It works" {
		t.Fatalf("ExtractAndStripAC ac = %q, want extracted criteria", ac)
	}
	if desc != "Build the thing." {
		t.Fatalf("ExtractAndStripAC desc = %q, want stripped description", desc)
	}
}
