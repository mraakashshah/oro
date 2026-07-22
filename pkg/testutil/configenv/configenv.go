// Package configenv isolates agent configuration from developer-global defaults in tests.
package configenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Run executes a package test runner with empty temporary HOME and ORO_HOME
// directories, then restores the caller's environment.
//
//oro:testonly
func Run(run func() int) int {
	root, err := os.MkdirTemp("", "oro-config-test-*")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create isolated config environment: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(root) }()

	home, homeSet := os.LookupEnv("HOME")
	oroHome, oroHomeSet := os.LookupEnv("ORO_HOME")
	goCache, goCacheSet := os.LookupEnv("GOCACHE")
	goModCache, goModCacheSet := os.LookupEnv("GOMODCACHE")
	defer restore("HOME", home, homeSet)
	defer restore("ORO_HOME", oroHome, oroHomeSet)
	defer restore("GOCACHE", goCache, goCacheSet)
	defer restore("GOMODCACHE", goModCache, goModCacheSet)

	if err := preserveGoCaches(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "preserve Go caches: %v\n", err)
		return 1
	}

	if err := os.Setenv("HOME", root); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "set isolated HOME: %v\n", err)
		return 1
	}
	if err := os.Setenv("ORO_HOME", filepath.Join(root, "oro-home")); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "set isolated ORO_HOME: %v\n", err)
		return 1
	}

	return run()
}

func preserveGoCaches() error {
	if os.Getenv("GOCACHE") != "" && os.Getenv("GOMODCACHE") != "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "env", "GOCACHE", "GOMODCACHE") //nolint:gosec // fixed Go command and arguments
	cmd.Env = append(os.Environ(), "GOTELEMETRY=off")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("resolve cache roots: %w", err)
	}
	roots := strings.Fields(string(output))
	if len(roots) != 2 {
		return fmt.Errorf("resolve cache roots: got %d values, want 2", len(roots))
	}
	if os.Getenv("GOCACHE") == "" {
		if err := os.Setenv("GOCACHE", roots[0]); err != nil {
			return fmt.Errorf("set GOCACHE: %w", err)
		}
	}
	if os.Getenv("GOMODCACHE") == "" {
		if err := os.Setenv("GOMODCACHE", roots[1]); err != nil {
			return fmt.Errorf("set GOMODCACHE: %w", err)
		}
	}
	return nil
}

func restore(key, value string, wasSet bool) {
	if wasSet {
		_ = os.Setenv(key, value)
		return
	}
	_ = os.Unsetenv(key)
}
