// Package main is the entry point for the oro CLI.
package main

import (
	"os"
)

func main() {
	cmd := newRootCmd()
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
