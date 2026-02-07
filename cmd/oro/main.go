// Package main is the entry point for the oro CLI.
package main

import (
	"fmt"

	"oro/internal/version"
)

func main() {
	fmt.Printf("oro %s\n", version.String())
}
