// Package configenv isolates agent configuration from developer-global defaults in tests.
package configenv

import (
	"fmt"
	"os"
	"path/filepath"
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
	defer restore("HOME", home, homeSet)
	defer restore("ORO_HOME", oroHome, oroHomeSet)

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

func restore(key, value string, wasSet bool) {
	if wasSet {
		_ = os.Setenv(key, value)
		return
	}
	_ = os.Unsetenv(key)
}
