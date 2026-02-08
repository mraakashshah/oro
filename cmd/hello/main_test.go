package main

import (
	"bytes"
	"io"
	"os"
	"testing"
)

func TestHelloWorldOutput(t *testing.T) {
	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Run main
	main()

	// Restore stdout
	if err := w.Close(); err != nil {
		t.Fatalf("failed to close pipe writer: %v", err)
	}
	os.Stdout = old

	// Read captured output
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("failed to read captured output: %v", err)
	}
	output := buf.String()

	expected := "Hello from oro worker!\n"
	if output != expected {
		t.Errorf("expected %q, got %q", expected, output)
	}
}
