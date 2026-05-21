package worker_test

import (
	"strings"
	"testing"

	"oro/pkg/worker"
)

func TestLineTailBufferKeepsLastCompleteLinesAndPartial(t *testing.T) {
	buf := worker.NewLineTailBuffer(2)
	if n, err := buf.Write([]byte("one\ntwo\r\nthree")); err != nil || n != len("one\ntwo\r\nthree") {
		t.Fatalf("Write returned n=%d err=%v", n, err)
	}
	if got, want := buf.String(), "two\nthree"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}

	if _, err := buf.Write([]byte("-partial\nfour\n")); err != nil {
		t.Fatalf("Write second chunk: %v", err)
	}
	if got, want := buf.String(), "three-partial\nfour"; got != want {
		t.Fatalf("String() after second chunk = %q, want %q", got, want)
	}
}

func TestLineTailBufferDefaultLimit(t *testing.T) {
	buf := worker.NewLineTailBuffer(0)
	for i := 0; i < 105; i++ {
		if _, err := buf.Write([]byte("x\n")); err != nil {
			t.Fatalf("Write line %d: %v", i, err)
		}
	}
	if got := strings.Count(buf.String(), "x"); got != 100 {
		t.Fatalf("retained line count = %d, want 100", got)
	}
}
