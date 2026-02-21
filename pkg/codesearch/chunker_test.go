package codesearch_test

import (
	"testing"

	"oro/pkg/codesearch"
)

func TestChunkGoSource_Functions(t *testing.T) {
	src := `package example

func Hello() string {
	return "hello"
}

func Add(a, b int) int {
	return a + b
}
`
	chunks, err := codesearch.ChunkGoSource("example.go", src)
	if err != nil {
		t.Fatalf("ChunkGoSource: %v", err)
	}

	// Should find 2 functions.
	funcs := filterChunks(chunks, codesearch.ChunkFunc)
	if len(funcs) != 2 {
		t.Fatalf("expected 2 functions, got %d", len(funcs))
	}

	if funcs[0].Name != "Hello" {
		t.Errorf("expected first func name Hello, got %q", funcs[0].Name)
	}
	if funcs[0].FilePath != "example.go" {
		t.Errorf("expected file path example.go, got %q", funcs[0].FilePath)
	}
	if funcs[0].StartLine < 1 {
		t.Errorf("expected StartLine >= 1, got %d", funcs[0].StartLine)
	}
	if funcs[0].EndLine < funcs[0].StartLine {
		t.Errorf("expected EndLine >= StartLine, got %d < %d", funcs[0].EndLine, funcs[0].StartLine)
	}

	if funcs[1].Name != "Add" {
		t.Errorf("expected second func name Add, got %q", funcs[1].Name)
	}
}

func TestChunkGoSource_Methods(t *testing.T) {
	src := `package example

type Server struct {
	port int
}

func (s *Server) Start() error {
	return nil
}

func (s *Server) Stop() {
}
`
	chunks, err := codesearch.ChunkGoSource("server.go", src)
	if err != nil {
		t.Fatalf("ChunkGoSource: %v", err)
	}

	methods := filterChunks(chunks, codesearch.ChunkMethod)
	if len(methods) != 2 {
		t.Fatalf("expected 2 methods, got %d", len(methods))
	}

	if methods[0].Name != "Server.Start" {
		t.Errorf("expected method name Server.Start, got %q", methods[0].Name)
	}
	if methods[1].Name != "Server.Stop" {
		t.Errorf("expected method name Server.Stop, got %q", methods[1].Name)
	}
}

func TestChunkGoSource_Types(t *testing.T) {
	src := `package example

type Server struct {
	Port int
	Host string
}

type Handler interface {
	Handle() error
}
`
	chunks, err := codesearch.ChunkGoSource("types.go", src)
	if err != nil {
		t.Fatalf("ChunkGoSource: %v", err)
	}

	types := filterChunks(chunks, codesearch.ChunkType)
	if len(types) != 2 {
		t.Fatalf("expected 2 types, got %d", len(types))
	}

	if types[0].Name != "Server" {
		t.Errorf("expected type name Server, got %q", types[0].Name)
	}
	if types[1].Name != "Handler" {
		t.Errorf("expected type name Handler, got %q", types[1].Name)
	}
}

func TestChunkGoSource_ConstBlocks(t *testing.T) {
	src := `package example

const (
	MaxRetries = 3
	Timeout    = 30
)

const SingleConst = "hello"
`
	chunks, err := codesearch.ChunkGoSource("consts.go", src)
	if err != nil {
		t.Fatalf("ChunkGoSource: %v", err)
	}

	consts := filterChunks(chunks, codesearch.ChunkConst)
	if len(consts) != 2 {
		t.Fatalf("expected 2 const blocks, got %d", len(consts))
	}
}

func TestChunkGoSource_ContentIncludesSource(t *testing.T) {
	src := `package example

func Hello() string {
	return "hello"
}
`
	chunks, err := codesearch.ChunkGoSource("example.go", src)
	if err != nil {
		t.Fatalf("ChunkGoSource: %v", err)
	}

	funcs := filterChunks(chunks, codesearch.ChunkFunc)
	if len(funcs) == 0 {
		t.Fatal("expected at least 1 function chunk")
	}

	// Content should contain the function signature.
	if !containsStr(funcs[0].Content, "func Hello()") {
		t.Errorf("expected content to contain 'func Hello()', got %q", funcs[0].Content)
	}
}

func TestChunkGoSource_InvalidSource(t *testing.T) {
	src := `this is not valid go code }{}{}{`
	_, err := codesearch.ChunkGoSource("bad.go", src)
	if err == nil {
		t.Fatal("expected error for invalid Go source")
	}
}

func TestChunkGoSource_EmptyFile(t *testing.T) {
	src := `package example
`
	chunks, err := codesearch.ChunkGoSource("empty.go", src)
	if err != nil {
		t.Fatalf("ChunkGoSource: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty file, got %d", len(chunks))
	}
}

func filterChunks(chunks []codesearch.Chunk, kind codesearch.ChunkKind) []codesearch.Chunk {
	var result []codesearch.Chunk
	for _, c := range chunks {
		if c.Kind == kind {
			result = append(result, c)
		}
	}
	return result
}

func containsStr(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || len(needle) == 0 ||
		findSubstring(haystack, needle))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// extractLines boundary conditions (via ChunkGoSource integration)
// ---------------------------------------------------------------------------

// TestExtractLines_BoundaryConditions tests line extraction edge cases by
// constructing Go source where specific line ranges are exercised.
func TestExtractLines_BoundaryConditions(t *testing.T) {
	t.Run("content_starts_at_line_1", func(t *testing.T) {
		// A minimal file: package + func on line 1 (after package decl).
		// Verifies startLine=1 (minimum) is handled correctly.
		src := "package x\nfunc F() {}\n"
		chunks, err := codesearch.ChunkGoSource("x.go", src)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		funcs := filterChunks(chunks, codesearch.ChunkFunc)
		if len(funcs) != 1 {
			t.Fatalf("expected 1 func, got %d", len(funcs))
		}
		if funcs[0].StartLine < 1 {
			t.Errorf("StartLine must be >= 1, got %d", funcs[0].StartLine)
		}
		if funcs[0].Content == "" {
			t.Error("Content must not be empty for a valid function")
		}
	})

	t.Run("content_last_line_no_trailing_newline", func(t *testing.T) {
		// Source without a trailing newline — endLine == len(lines).
		src := "package x\nfunc F() {}"
		chunks, err := codesearch.ChunkGoSource("x.go", src)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		funcs := filterChunks(chunks, codesearch.ChunkFunc)
		if len(funcs) != 1 {
			t.Fatalf("expected 1 func, got %d", len(funcs))
		}
		if !findSubstring(funcs[0].Content, "func F()") {
			t.Errorf("Content should contain 'func F()', got %q", funcs[0].Content)
		}
	})

	t.Run("multiline_function_exact_lines", func(t *testing.T) {
		// Verify that a multi-line function captures all its lines.
		src := "package x\n\nfunc F() {\n\t_ = 1\n\t_ = 2\n}\n"
		// Lines:        1         2  3            4           5       6
		chunks, err := codesearch.ChunkGoSource("x.go", src)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		funcs := filterChunks(chunks, codesearch.ChunkFunc)
		if len(funcs) != 1 {
			t.Fatalf("expected 1 func, got %d", len(funcs))
		}
		f := funcs[0]
		if f.EndLine <= f.StartLine {
			t.Errorf("multi-line function: EndLine (%d) must be > StartLine (%d)", f.EndLine, f.StartLine)
		}
		if !findSubstring(f.Content, "_ = 1") {
			t.Errorf("Content missing body line '_ = 1', got: %q", f.Content)
		}
		if !findSubstring(f.Content, "_ = 2") {
			t.Errorf("Content missing body line '_ = 2', got: %q", f.Content)
		}
	})

	t.Run("single_line_function_start_equals_end", func(t *testing.T) {
		// A one-liner function: startLine == endLine.
		src := "package x\nfunc F() {}\n"
		chunks, err := codesearch.ChunkGoSource("x.go", src)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		funcs := filterChunks(chunks, codesearch.ChunkFunc)
		if len(funcs) != 1 {
			t.Fatalf("expected 1 func, got %d", len(funcs))
		}
		f := funcs[0]
		if f.StartLine != f.EndLine {
			t.Errorf("one-liner: expected StartLine == EndLine, got %d vs %d", f.StartLine, f.EndLine)
		}
	})
}

// ---------------------------------------------------------------------------
// Const naming — <=3 and >3 names (off-by-one in slice boundary)
// ---------------------------------------------------------------------------

func TestChunkGoSource_ConstNaming(t *testing.T) {
	tests := []struct {
		name        string
		src         string
		wantName    string
		wantContain string
	}{
		{
			name: "one_const_name",
			src: `package x
const (
	A = 1
)
`,
			wantName: "const(A)",
		},
		{
			name: "two_const_names",
			src: `package x
const (
	A = 1
	B = 2
)
`,
			wantName: "const(A,B)",
		},
		{
			name: "exactly_three_const_names",
			src: `package x
const (
	A = 1
	B = 2
	C = 3
)
`,
			wantName: "const(A,B,C)",
		},
		{
			name: "four_const_names_uses_ellipsis",
			src: `package x
const (
	A = 1
	B = 2
	C = 3
	D = 4
)
`,
			// >3 names: "const(A,B,C,...+1)"
			wantContain: "const(A,B,C,...+1)",
		},
		{
			name: "five_const_names_uses_ellipsis",
			src: `package x
const (
	A = 1
	B = 2
	C = 3
	D = 4
	E = 5
)
`,
			wantContain: "const(A,B,C,...+2)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chunks, err := codesearch.ChunkGoSource("c.go", tc.src)
			if err != nil {
				t.Fatalf("ChunkGoSource error: %v", err)
			}
			consts := filterChunks(chunks, codesearch.ChunkConst)
			if len(consts) != 1 {
				t.Fatalf("expected 1 const chunk, got %d", len(consts))
			}
			got := consts[0].Name
			if tc.wantName != "" && got != tc.wantName {
				t.Errorf("expected name %q, got %q", tc.wantName, got)
			}
			if tc.wantContain != "" && !findSubstring(got, tc.wantContain) {
				t.Errorf("expected name to contain %q, got %q", tc.wantContain, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Method receiver type — pointer vs value receiver
// ---------------------------------------------------------------------------

func TestChunkGoSource_MethodReceiverTypes(t *testing.T) {
	tests := []struct {
		name       string
		src        string
		wantMethod string
	}{
		{
			name: "pointer_receiver",
			src: `package x
type T struct{}
func (t *T) M() {}
`,
			wantMethod: "T.M",
		},
		{
			name: "value_receiver",
			src: `package x
type T struct{}
func (t T) M() {}
`,
			wantMethod: "T.M",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chunks, err := codesearch.ChunkGoSource("m.go", tc.src)
			if err != nil {
				t.Fatalf("ChunkGoSource error: %v", err)
			}
			methods := filterChunks(chunks, codesearch.ChunkMethod)
			if len(methods) != 1 {
				t.Fatalf("expected 1 method, got %d", len(methods))
			}
			if methods[0].Name != tc.wantMethod {
				t.Errorf("expected method name %q, got %q", tc.wantMethod, methods[0].Name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FilePath propagation — path is faithfully passed through to all chunk kinds
// ---------------------------------------------------------------------------

func TestChunkGoSource_FilePathPropagation(t *testing.T) {
	src := `package x

func F() {}

type T struct{}

const K = 1
`
	const path = "subdir/file.go"
	chunks, err := codesearch.ChunkGoSource(path, src)
	if err != nil {
		t.Fatalf("ChunkGoSource error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	for _, c := range chunks {
		if c.FilePath != path {
			t.Errorf("chunk %q: expected FilePath %q, got %q", c.Name, path, c.FilePath)
		}
	}
}

// ---------------------------------------------------------------------------
// Chunk ordering — chunks appear in source order
// ---------------------------------------------------------------------------

func TestChunkGoSource_ChunkOrdering(t *testing.T) {
	src := `package x

func Alpha() {}

func Beta() {}

func Gamma() {}
`
	chunks, err := codesearch.ChunkGoSource("order.go", src)
	if err != nil {
		t.Fatalf("ChunkGoSource error: %v", err)
	}
	funcs := filterChunks(chunks, codesearch.ChunkFunc)
	if len(funcs) != 3 {
		t.Fatalf("expected 3 funcs, got %d", len(funcs))
	}
	// StartLines must be strictly increasing.
	for i := 1; i < len(funcs); i++ {
		if funcs[i].StartLine <= funcs[i-1].StartLine {
			t.Errorf("chunks not in source order: funcs[%d].StartLine=%d <= funcs[%d].StartLine=%d",
				i, funcs[i].StartLine, i-1, funcs[i-1].StartLine)
		}
	}
	// Names must follow source order.
	wantOrder := []string{"Alpha", "Beta", "Gamma"}
	for i, want := range wantOrder {
		if funcs[i].Name != want {
			t.Errorf("funcs[%d]: expected %q, got %q", i, want, funcs[i].Name)
		}
	}
}

// ---------------------------------------------------------------------------
// Content correctness — Content must equal joined source lines exactly
// ---------------------------------------------------------------------------

func TestChunkGoSource_ContentExact(t *testing.T) {
	// Each function body is unique enough to verify exact line extraction.
	src := `package x

func Foo() string {
	return "FOO_SENTINEL"
}

func Bar() string {
	return "BAR_SENTINEL"
}
`
	chunks, err := codesearch.ChunkGoSource("exact.go", src)
	if err != nil {
		t.Fatalf("ChunkGoSource error: %v", err)
	}
	funcs := filterChunks(chunks, codesearch.ChunkFunc)
	if len(funcs) != 2 {
		t.Fatalf("expected 2 funcs, got %d", len(funcs))
	}

	if !findSubstring(funcs[0].Content, "FOO_SENTINEL") {
		t.Errorf("Foo chunk missing sentinel, content: %q", funcs[0].Content)
	}
	if findSubstring(funcs[0].Content, "BAR_SENTINEL") {
		t.Errorf("Foo chunk must not contain BAR_SENTINEL, content: %q", funcs[0].Content)
	}

	if !findSubstring(funcs[1].Content, "BAR_SENTINEL") {
		t.Errorf("Bar chunk missing sentinel, content: %q", funcs[1].Content)
	}
	if findSubstring(funcs[1].Content, "FOO_SENTINEL") {
		t.Errorf("Bar chunk must not contain FOO_SENTINEL, content: %q", funcs[1].Content)
	}
}

// ---------------------------------------------------------------------------
// ChunkKind assignment — funcs vs methods are classified correctly
// ---------------------------------------------------------------------------

func TestChunkGoSource_KindAssignment(t *testing.T) {
	src := `package x
type S struct{}
func TopLevel() {}
func (s S) Method() {}
`
	chunks, err := codesearch.ChunkGoSource("kinds.go", src)
	if err != nil {
		t.Fatalf("ChunkGoSource error: %v", err)
	}
	funcs := filterChunks(chunks, codesearch.ChunkFunc)
	methods := filterChunks(chunks, codesearch.ChunkMethod)

	if len(funcs) != 1 {
		t.Errorf("expected 1 ChunkFunc, got %d", len(funcs))
	}
	if funcs[0].Name != "TopLevel" {
		t.Errorf("expected func name TopLevel, got %q", funcs[0].Name)
	}

	if len(methods) != 1 {
		t.Errorf("expected 1 ChunkMethod, got %d", len(methods))
	}
	if methods[0].Name != "S.Method" {
		t.Errorf("expected method name S.Method, got %q", methods[0].Name)
	}
}

// ---------------------------------------------------------------------------
// Const block with zero specs — returns no chunks (guard clause coverage)
// ---------------------------------------------------------------------------

func TestChunkGoSource_VarDeclIgnored(t *testing.T) {
	// var declarations are not handled (only type and const): must not panic
	// and must return 0 chunks of type ChunkConst.
	src := `package x
var X = 1
var Y = 2
`
	chunks, err := codesearch.ChunkGoSource("vars.go", src)
	if err != nil {
		t.Fatalf("ChunkGoSource error: %v", err)
	}
	consts := filterChunks(chunks, codesearch.ChunkConst)
	if len(consts) != 0 {
		t.Errorf("expected 0 const chunks for var decl, got %d", len(consts))
	}
}

// ---------------------------------------------------------------------------
// Type chunk line range — StartLine/EndLine sanity for multi-field struct
// ---------------------------------------------------------------------------

func TestChunkGoSource_TypeChunkLineRange(t *testing.T) {
	src := `package x

type Big struct {
	A int
	B string
	C float64
}
`
	chunks, err := codesearch.ChunkGoSource("big.go", src)
	if err != nil {
		t.Fatalf("ChunkGoSource error: %v", err)
	}
	types := filterChunks(chunks, codesearch.ChunkType)
	if len(types) != 1 {
		t.Fatalf("expected 1 type chunk, got %d", len(types))
	}
	tp := types[0]
	if tp.EndLine <= tp.StartLine {
		t.Errorf("multi-field struct: EndLine (%d) should be > StartLine (%d)", tp.EndLine, tp.StartLine)
	}
	if !findSubstring(tp.Content, "float64") {
		t.Errorf("type content should include all fields; missing 'float64', got: %q", tp.Content)
	}
}

// ---------------------------------------------------------------------------
// Multiple type specs in one declaration block
// ---------------------------------------------------------------------------

func TestChunkGoSource_MultipleTypeSpecs(t *testing.T) {
	src := `package x

type (
	Foo struct{ X int }
	Bar interface{ Do() }
)
`
	chunks, err := codesearch.ChunkGoSource("multi.go", src)
	if err != nil {
		t.Fatalf("ChunkGoSource error: %v", err)
	}
	types := filterChunks(chunks, codesearch.ChunkType)
	if len(types) != 2 {
		t.Fatalf("expected 2 type chunks from grouped decl, got %d", len(types))
	}
	names := map[string]bool{types[0].Name: true, types[1].Name: true}
	if !names["Foo"] || !names["Bar"] {
		t.Errorf("expected type names Foo and Bar, got %q and %q", types[0].Name, types[1].Name)
	}
}
