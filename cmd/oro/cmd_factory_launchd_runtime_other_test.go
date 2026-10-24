//go:build !darwin

package main

import "testing"

func testFactoryHostScopePlatform(t *testing.T) {
	if runtime, err := openFactoryLaunchdRuntime(t.Context(), factoryLaunchdConfig{}); err == nil || runtime != nil {
		t.Fatal("unsupported platform admitted launchd runtime")
	}
}
