package memory

import (
	"context"
	"path/filepath"

	"oro/pkg/modelartifacts"
)

// ErrDigestMismatch is returned when a downloaded model's SHA256 does not match
// the expected digest in the ModelSpec.
var ErrDigestMismatch = modelartifacts.ErrDigestMismatch

// ModelSpec describes a single model artifact to download and verify.
type ModelSpec = modelartifacts.ModelSpec

// KnownModels lists the ONNX model artifacts required for semantic memory.
// SHA256 digests are pinned to specific releases; update when upgrading models.
var KnownModels = modelartifacts.KnownModels //nolint:gochecknoglobals // compatibility alias for existing callers

// ModelPath returns the path for a model's primary ONNX file within modelDir.
//
//oro:testonly
func ModelPath(modelDir, name string) string {
	return filepath.Join(modelDir, name, "model.onnx")
}

// VerifyModel returns nil if the file at path has the expected SHA256 hex digest.
func VerifyModel(path, expectedSHA256 string) error {
	// Preserve the historical memory-package error text for callers.
	//nolint:wrapcheck // compatibility wrapper must return the underlying error unchanged
	return modelartifacts.VerifyModel(path, expectedSHA256)
}

// PrefetchModels downloads and verifies each spec into modelDir/<name>/<filename>.
// Empty modelDir defaults to ~/.oro/models.
// Skips specs whose file already exists with a matching digest.
// On digest mismatch, renames the file to <path>.corrupt and returns a wrapped ErrDigestMismatch.
// On context cancellation mid-download, removes the partial file and returns ctx.Err().
//
//oro:testonly
func PrefetchModels(ctx context.Context, modelDir string, specs []ModelSpec) error {
	// Preserve the historical memory-package error text for callers.
	//nolint:wrapcheck // compatibility wrapper must return the underlying error unchanged
	return modelartifacts.PrefetchModels(ctx, modelDir, specs)
}
