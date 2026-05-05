//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// impactOut mirrors the JSON schema produced by newImpactCmd.
type impactOut struct {
	Symbol            string   `json:"symbol"`
	File              string   `json:"file"`
	DirectCallers     []string `json:"direct_callers"`
	TransitiveCallers []string `json:"transitive_callers"`
	CrossPkgCallees   []string `json:"cross_package_callees"`
	ExternalCallees   []string `json:"external_callees"`
}

func TestImpactCommand(t *testing.T) {
	fixtureDir, err := filepath.Abs(filepath.Join("testdata", "impact_fixture"))
	require.NoError(t, err)

	targetArg := filepath.Join(fixtureDir, "pkg", "dispatcher", "dispatcher.go") + ":Dispatcher.Run"

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"impact", targetArg})

	require.NoError(t, root.Execute(), "oro impact must not error")

	var got impactOut
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got), "output must be valid JSON")

	// direct_callers: non-empty, sorted, deduped.
	require.NotEmpty(t, got.DirectCallers, "direct_callers must be non-empty")
	assert.True(t, sort.StringsAreSorted(got.DirectCallers), "direct_callers must be sorted")
	assert.Equal(t, dedupStrings(got.DirectCallers), got.DirectCallers, "direct_callers must be deduped")

	// transitive_callers: distinct from direct_callers.
	directSet := make(map[string]bool, len(got.DirectCallers))
	for _, c := range got.DirectCallers {
		directSet[c] = true
	}
	for _, tc := range got.TransitiveCallers {
		assert.False(t, directSet[tc], "transitive caller %q must not appear in direct_callers", tc)
	}

	// cross_package_callees must be non-empty.
	assert.NotEmpty(t, got.CrossPkgCallees, "cross_package_callees must be non-empty")

	// external_callees must be non-empty.
	assert.NotEmpty(t, got.ExternalCallees, "external_callees must be non-empty")

	// Schema validates against golden file.
	goldenPath := filepath.Join(fixtureDir, "expected.json")
	golden, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "expected.json must exist; run the test with -update to regenerate")

	var want impactOut
	require.NoError(t, json.Unmarshal(golden, &want))
	assert.Equal(t, want, got, "output must match expected.json golden file")
}

func dedupStrings(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	out := ss[:0:0]
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
