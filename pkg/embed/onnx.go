// Package embed provides embedding model adapters for semantic recall.
package embeddings

const (
	bgeSmallName = "bge-small-en-v1.5"
	bgeSmallDim  = 384
)

// ONNXEmbedder embeds text with the bge-small-en-v1.5 ONNX model.
//
//oro:testonly -- wired into dispatcher semantic recall by the next Phase 2 bead.
type ONNXEmbedder struct{}
