package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllHookInstallersUseStorageExec(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		contents string
	}{
		{
			name:     "standard pre-push wrapper",
			contents: buildOroPrePushCheck(""),
		},
		{
			name:     "stealth pre-push wrapper",
			contents: buildOroPrePushCheck(filepath.Join(t.TempDir(), "quality_gate.sh")),
		},
		{
			name:     "checked-in pre-push template",
			contents: mustReadHookInstaller(t, filepath.Join("..", "..", "git", "hooks", "pre-push")),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assertQualityGateUsesStorageExec(t, test.contents)
		})
	}

	makefile := mustReadHookInstaller(t, filepath.Join("..", "..", "Makefile"))
	_, installTarget, found := strings.Cut(makefile, "install-git-hooks:")
	if !found {
		t.Fatal("Makefile must define install-git-hooks")
	}
	if !strings.Contains(installTarget, "git/hooks/*") {
		t.Fatal("install-git-hooks must install the checked-in hook templates")
	}
}

func assertQualityGateUsesStorageExec(t *testing.T, contents string) {
	t.Helper()
	if !strings.Contains(contents, "oro storage exec --workdir") {
		t.Fatalf("quality gate hook must use oro storage exec:\n%s", contents)
	}
	if strings.Contains(contents, "ORO_QG_CONTEXT=push \"$") ||
		strings.Contains(contents, "ORO_QG_CONTEXT=push scripts/quality_gate.sh") {
		t.Fatalf("quality gate hook must not invoke the quality gate directly:\n%s", contents)
	}
}

func mustReadHookInstaller(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // test reads checked-in installer paths
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
