package factory //nolint:testpackage // The structural guard lives alongside the private lifecycle implementation.

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestFactoryDependencyBoundary(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("read factory sources: %v", err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		source, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, dependency := range source.Imports {
			name, err := strconv.Unquote(dependency.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if !factoryImportAllowed(name) {
				t.Errorf("%s imports %s: factory may depend only on standard library and oro/pkg/dbutil", path, name)
			}
		}
	}
}

func factoryImportAllowed(path string) bool {
	if path == "oro/pkg/dbutil" {
		return true
	}
	info, err := os.Stat(filepath.Join(runtime.GOROOT(), "src", filepath.FromSlash(path))) //nolint:staticcheck // This source guard runs in the build checkout with its producing toolchain, not as a relocated binary.
	return err == nil && info.IsDir()
}

func TestFactoryDependencyGuard(t *testing.T) {
	for _, path := range []string{"oro/pkg/dispatcher", "oro/pkg/worker", "oro/pkg/ops", "oro/pkg/processenv", "oro/pkg/storage", "github.com/spf13/cobra"} {
		if factoryImportAllowed(path) {
			t.Errorf("dependency guard allowed %s", path)
		}
	}
	for _, path := range []string{"context", "encoding/json", "oro/pkg/dbutil"} {
		if !factoryImportAllowed(path) {
			t.Errorf("dependency guard rejected %s", path)
		}
	}
}
