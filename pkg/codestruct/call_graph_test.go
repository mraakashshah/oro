package codestruct_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"oro/pkg/codestruct"
)

const (
	cgAlphaFile = "testdata/callgraph/alpha/alpha.go"
	cgBetaFile  = "testdata/callgraph/beta/beta.go"
)

func TestCallGraph(t *testing.T) {
	alphaSyms, err := codestruct.ExtractGoSymbols(cgAlphaFile)
	require.NoError(t, err)
	betaSyms, err := codestruct.ExtractGoSymbols(cgBetaFile)
	require.NoError(t, err)

	pkgSymbols := map[string][]codestruct.Symbol{
		cgAlphaFile: alphaSyms,
		cgBetaFile:  betaSyms,
	}

	t.Run("in_project_same_package_edges", func(t *testing.T) {
		edges, _, err := codestruct.BuildCallGraph(
			[]string{cgAlphaFile},
			pkgSymbols,
		)
		require.NoError(t, err)

		edge := findCallEdge(edges, "Alpha", "Beta")
		require.NotNil(t, edge, "expected edge Alpha→Beta, got: %+v", edges)
		assert.True(t, edge.Resolved, "Alpha→Beta should be resolved")
		assert.Equal(t, cgAlphaFile, edge.CalleeFile)
	})

	t.Run("cross_package_resolution", func(t *testing.T) {
		edges, _, err := codestruct.BuildCallGraph(
			[]string{cgBetaFile},
			pkgSymbols,
		)
		require.NoError(t, err)

		edge := findCallEdge(edges, "Gamma", "Alpha")
		require.NotNil(t, edge, "expected edge Gamma→Alpha, got: %+v", edges)
		assert.True(t, edge.Resolved, "Gamma→Alpha should be resolved")
		assert.Equal(t, cgAlphaFile, edge.CalleeFile)
	})

	t.Run("unresolved_callees_logged", func(t *testing.T) {
		_, warnings, err := codestruct.BuildCallGraph(
			[]string{cgAlphaFile},
			pkgSymbols,
		)
		require.NoError(t, err)

		found := false
		for _, w := range warnings {
			if strings.Contains(w, "Println") {
				found = true
				break
			}
		}
		assert.True(t, found, "expected warning about unresolved Println, got: %v", warnings)
	})
}

func findCallEdge(edges []codestruct.CallEdge, callerSym, calleeSym string) *codestruct.CallEdge {
	for i := range edges {
		if edges[i].CallerSymbol == callerSym && edges[i].CalleeSymbol == calleeSym {
			return &edges[i]
		}
	}
	return nil
}
