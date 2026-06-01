package codestruct_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"oro/pkg/codestruct"
)

func TestResolveCallee_FilePathFirst(t *testing.T) {
	const (
		callerFile = "pkg/app/app.go"
		helperFile = "pkg/app/helper.go"
		importFile = "pkg/build/build.go"
		otherFile  = "pkg/other/build.go"
	)

	importsByFile := map[string]map[string]string{
		callerFile: {"build": "build"},
	}
	symsByFile := map[string][]codestruct.Symbol{
		callerFile: {{Name: "build", Kind: codestruct.KindFunc}},
		helperFile: {{Name: "new", Kind: codestruct.KindFunc}},
		importFile: {{Name: "New", Kind: codestruct.KindFunc}},
		otherFile:  {{Name: "New", Kind: codestruct.KindFunc}},
	}

	t.Run("resolved edge returns canonical file ref", func(t *testing.T) {
		ref, ok := codestruct.ResolveCallee(codestruct.CallEdge{
			CallerFile:   callerFile,
			CalleeName:   "build",
			CalleeFile:   helperFile,
			CalleeSymbol: "new",
			Resolved:     true,
		}, importsByFile, symsByFile)

		require.True(t, ok)
		assert.Equal(t, helperFile+":new", ref)
	})

	t.Run("same file bare symbol wins over global bare names", func(t *testing.T) {
		ref, ok := codestruct.ResolveCallee(codestruct.CallEdge{
			CallerFile: callerFile,
			CalleeName: "build",
		}, importsByFile, symsByFile)

		require.True(t, ok)
		assert.Equal(t, callerFile+":build", ref)
	})

	t.Run("import-qualified symbol is scoped to imported package", func(t *testing.T) {
		ref, ok := codestruct.ResolveCallee(codestruct.CallEdge{
			CallerFile: callerFile,
			CalleeName: "build.New",
		}, importsByFile, symsByFile)

		require.True(t, ok)
		assert.Equal(t, importFile+":New", ref)
	})
}

func TestResolveCallee_AmbiguousGivesUp(t *testing.T) {
	const callerFile = "pkg/app/app.go"

	ref, ok := codestruct.ResolveCallee(codestruct.CallEdge{
		CallerFile: callerFile,
		CalleeName: "build",
	}, nil, map[string][]codestruct.Symbol{
		"pkg/one/one.go": {{Name: "build", Kind: codestruct.KindFunc}},
		"pkg/two/two.go": {{Name: "build", Kind: codestruct.KindFunc}},
	})

	assert.False(t, ok)
	assert.Empty(t, ref)
}
