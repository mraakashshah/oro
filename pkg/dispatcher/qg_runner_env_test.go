package dispatcher_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/dispatcher"
)

func TestShellQGRunnerNormalizesInvalidLocaleBeforeStartingBash(t *testing.T) {
	t.Setenv("LC_ALL", "oro_invalid_locale_for_test.UTF-8")
	t.Setenv("LANG", "oro_invalid_locale_for_test.UTF-8")

	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "quality_gate.sh")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\nprintf 'locale=%s lang=%s\\n' \"${LC_ALL:-}\" \"${LANG:-}\"\n"), 0o600); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o755); err != nil { //nolint:gosec // test script must be executable
		t.Fatal(err)
	}

	passed, output, err := (&dispatcher.ShellQGRunner{}).Run(context.Background(), tmpDir, false)
	if err != nil {
		t.Fatalf("ShellQGRunner.Run: %v", err)
	}
	if !passed {
		t.Fatalf("expected quality gate to pass, output: %s", output)
	}
	if strings.Contains(output, "setlocale") {
		t.Fatalf("quality gate inherited invalid locale before bash startup: %q", output)
	}
	if !strings.Contains(output, "locale=C lang=C") {
		t.Fatalf("quality gate did not normalize locale env, output: %q", output)
	}
}
