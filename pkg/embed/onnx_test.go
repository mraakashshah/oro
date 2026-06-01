package embeddings_test

import (
	"reflect"
	"testing"

	embed "oro/pkg/embed"
)

func TestONNXEmbedder_DimIs384(t *testing.T) {
	e := &embed.ONNXEmbedder{}

	if got := e.Dim(); got != 384 {
		t.Fatalf("Dim() = %d, want 384", got)
	}
}

func TestONNXEmbedder_Deterministic(t *testing.T) {
	e := &embed.ONNXEmbedder{}

	first := e.Embed("same text")
	second := e.Embed("same text")

	if len(first) != e.Dim() {
		t.Fatalf("len(Embed()) = %d, want %d", len(first), e.Dim())
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("Embed returned different vectors for identical text")
	}
}
