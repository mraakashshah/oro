package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDoctrineAuditCommand(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"doctrine", "audit"})

	if err := root.Execute(); err != nil {
		t.Fatalf("doctrine audit failed: %v\n%s", err, out.String())
	}

	got := out.String()
	for _, want := range []string{
		"Doctrine audit PASS",
		"rules: 60",
		"level-6 promotion paths: 15",
		"assets/rules-audit.md",
		"assets/doctrine.md",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("doctrine audit output missing %q:\n%s", want, got)
		}
	}
}
