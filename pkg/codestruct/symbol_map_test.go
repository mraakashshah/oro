package codestruct_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"oro/pkg/codestruct"
)

func TestGoSymbolMap(t *testing.T) {
	symbols, err := codestruct.ExtractGoSymbols("testdata/sample.go")
	require.NoError(t, err)
	require.NotEmpty(t, symbols)

	byName := make(map[string]*codestruct.Symbol, len(symbols))
	for i := range symbols {
		byName[symbols[i].Name] = &symbols[i]
	}

	cases := []struct {
		name       string
		kind       codestruct.SymbolKind
		visibility string
		receiver   string // non-empty means we check it
	}{
		{name: "PublicFunc", kind: codestruct.KindFunc, visibility: "exported"},
		{name: "privateFunc", kind: codestruct.KindFunc, visibility: "unexported"},
		{name: "Method", kind: codestruct.KindMethod, visibility: "exported", receiver: "*MyStruct"},
		{name: "privateMethod", kind: codestruct.KindMethod, visibility: "unexported", receiver: "MyStruct"},
		{name: "MyStruct", kind: codestruct.KindType, visibility: "exported"},
		{name: "myStruct", kind: codestruct.KindType, visibility: "unexported"},
		{name: "MyInterface", kind: codestruct.KindInterface, visibility: "exported"},
		{name: "MyConst", kind: codestruct.KindConst, visibility: "exported"},
		{name: "myConst", kind: codestruct.KindConst, visibility: "unexported"},
		{name: "MyVar", kind: codestruct.KindVar, visibility: "exported"},
		{name: "myVar", kind: codestruct.KindVar, visibility: "unexported"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, ok := byName[tc.name]
			require.True(t, ok, "symbol %q not found in %v", tc.name, symbolNames(symbols))
			assert.Equal(t, tc.kind, s.Kind)
			assert.Equal(t, tc.visibility, s.Visibility)
			if tc.receiver != "" {
				assert.Equal(t, tc.receiver, s.Receiver)
			}
			assert.NotEmpty(t, s.Signature, "Signature should not be empty")
			assert.Greater(t, s.LineStart, 0, "LineStart must be > 0")
			assert.GreaterOrEqual(t, s.LineEnd, s.LineStart, "LineEnd >= LineStart")
		})
	}
}

func symbolNames(syms []codestruct.Symbol) []string {
	names := make([]string, len(syms))
	for i, s := range syms {
		names[i] = s.Name
	}
	return names
}
