//go:build cgo

package codestruct_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"oro/pkg/codestruct"
)

func TestPySymbolMap(t *testing.T) {
	symbols, err := codestruct.ExtractPySymbols("testdata/sample.py")
	require.NoError(t, err)
	require.NotEmpty(t, symbols)

	byName := indexByName(symbols)

	cases := []struct {
		name       string
		kind       codestruct.SymbolKind
		visibility string
		receiver   string
	}{
		{name: "public_func", kind: codestruct.KindFunc, visibility: "exported"},
		{name: "async_func", kind: codestruct.KindFunc, visibility: "exported"},
		{name: "_private_func", kind: codestruct.KindFunc, visibility: "unexported"},
		{name: "decorated_func", kind: codestruct.KindFunc, visibility: "exported"},
		{name: "MyClass", kind: codestruct.KindClass, visibility: "exported"},
		{name: "_PrivateClass", kind: codestruct.KindClass, visibility: "unexported"},
		{name: "public_method", kind: codestruct.KindMethod, visibility: "exported", receiver: "MyClass"},
		{name: "_private_method", kind: codestruct.KindMethod, visibility: "unexported", receiver: "MyClass"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, ok := byName[tc.name]
			require.True(t, ok, "symbol %q not found; got: %v", tc.name, symbolNames(symbols))
			assert.Equal(t, tc.kind, s.Kind)
			assert.Equal(t, tc.visibility, s.Visibility)
			if tc.receiver != "" {
				assert.Equal(t, tc.receiver, s.Receiver)
			}
			assert.NotEmpty(t, s.Signature)
			assert.Greater(t, s.LineStart, 0)
			assert.GreaterOrEqual(t, s.LineEnd, s.LineStart)
		})
	}
}

func TestTSSymbolMap(t *testing.T) {
	symbols, err := codestruct.ExtractTSSymbols("testdata/sample.ts")
	require.NoError(t, err)
	require.NotEmpty(t, symbols)

	byName := indexByName(symbols)

	cases := []struct {
		name       string
		kind       codestruct.SymbolKind
		visibility string
		receiver   string
	}{
		{name: "publicFunc", kind: codestruct.KindFunc, visibility: "exported"},
		{name: "unexportedFunc", kind: codestruct.KindFunc, visibility: "unexported"},
		{name: "MyClass", kind: codestruct.KindClass, visibility: "exported"},
		{name: "publicMethod", kind: codestruct.KindMethod, visibility: "exported", receiver: "MyClass"},
		{name: "privateMethod", kind: codestruct.KindMethod, visibility: "private", receiver: "MyClass"},
		{name: "MyInterface", kind: codestruct.KindInterface, visibility: "exported"},
		{name: "MyType", kind: codestruct.KindType, visibility: "exported"},
		{name: "arrowFunc", kind: codestruct.KindFunc, visibility: "exported"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, ok := byName[tc.name]
			require.True(t, ok, "symbol %q not found; got: %v", tc.name, symbolNames(symbols))
			assert.Equal(t, tc.kind, s.Kind)
			assert.Equal(t, tc.visibility, s.Visibility)
			if tc.receiver != "" {
				assert.Equal(t, tc.receiver, s.Receiver)
			}
			assert.NotEmpty(t, s.Signature)
			assert.Greater(t, s.LineStart, 0)
			assert.GreaterOrEqual(t, s.LineEnd, s.LineStart)
		})
	}
}

func TestJSSymbolMap(t *testing.T) {
	symbols, err := codestruct.ExtractJSSymbols("testdata/sample.js")
	require.NoError(t, err)
	require.NotEmpty(t, symbols)

	byName := indexByName(symbols)

	cases := []struct {
		name       string
		kind       codestruct.SymbolKind
		visibility string
		receiver   string
	}{
		{name: "publicFunc", kind: codestruct.KindFunc, visibility: "exported"},
		{name: "MyClass", kind: codestruct.KindClass, visibility: "exported"},
		{name: "publicMethod", kind: codestruct.KindMethod, visibility: "exported", receiver: "MyClass"},
		{name: "anotherMethod", kind: codestruct.KindMethod, visibility: "exported", receiver: "MyClass"},
		{name: "arrowFunc", kind: codestruct.KindFunc, visibility: "exported"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, ok := byName[tc.name]
			require.True(t, ok, "symbol %q not found; got: %v", tc.name, symbolNames(symbols))
			assert.Equal(t, tc.kind, s.Kind)
			assert.Equal(t, tc.visibility, s.Visibility)
			if tc.receiver != "" {
				assert.Equal(t, tc.receiver, s.Receiver)
			}
			assert.NotEmpty(t, s.Signature)
			assert.Greater(t, s.LineStart, 0)
			assert.GreaterOrEqual(t, s.LineEnd, s.LineStart)
		})
	}
}

func indexByName(syms []codestruct.Symbol) map[string]*codestruct.Symbol {
	m := make(map[string]*codestruct.Symbol, len(syms))
	for i := range syms {
		m[syms[i].Name] = &syms[i]
	}
	return m
}
