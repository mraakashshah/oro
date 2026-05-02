package main

import (
	"fmt"
	"os"
	"strings"
)

func nativeProductionBeadSourceMode() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("ORO_BEADSOURCE_MODE")))
	if mode == "" {
		return "sqlite"
	}
	return mode
}

func requireNativeProductionBeadSourceMode(command string) error {
	mode := nativeProductionBeadSourceMode()
	switch mode {
	case "sqlite":
		return nil
	case "cli", "shadow":
		return fmt.Errorf("%s requires native sqlite beadstore; ORO_BEADSOURCE_MODE=%s is no longer supported for production dispatcher/work", command, mode)
	default:
		return fmt.Errorf("unknown ORO_BEADSOURCE_MODE %q", mode)
	}
}
