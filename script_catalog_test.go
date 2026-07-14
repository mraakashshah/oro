package main

import (
	"os"
	"strings"
	"testing"
)

func TestScriptCatalogUsesInterpreterForNonExecutableShellScript(t *testing.T) {
	const scriptPath = "scripts/nilaway_lint_wiring_test.sh"
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat %s: %v", scriptPath, err)
	}
	if info.Mode().Perm()&0o111 != 0 {
		return
	}

	catalog, err := os.ReadFile("scripts/README.md")
	if err != nil {
		t.Fatalf("read script catalog: %v", err)
	}
	if !strings.Contains(string(catalog), "`bash "+scriptPath+"`") {
		t.Fatalf("non-executable %s must be invoked through bash in scripts/README.md", scriptPath)
	}
}
