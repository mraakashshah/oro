package codestruct_test

import (
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/codestruct"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeImpactWalkError(t *testing.T) {
	_, err := codestruct.ComputeImpact(filepath.Join(t.TempDir(), "does-not-exist"), "ignored.go", "Run")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "impact:")
}

func TestFindGoModDir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module find\n\ngo 1.21\n"), 0o600))
	nested := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	got, err := codestruct.FindGoModDir(nested)
	require.NoError(t, err)
	gotResolved, err := filepath.EvalSymlinks(got)
	require.NoError(t, err)
	rootResolved, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	assert.Equal(t, rootResolved, gotResolved)
}

func TestFindGoModDirNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := codestruct.FindGoModDir(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no go.mod found")
}
