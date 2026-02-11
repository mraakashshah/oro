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
