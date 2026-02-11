package codesearch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// ChunkKind identifies the type of code chunk extracted from a source file.
type ChunkKind string

const (
	// ChunkFunc is a top-level function declaration.
	ChunkFunc ChunkKind = "func"
	// ChunkMethod is a method (function with receiver).
	ChunkMethod ChunkKind = "method"
	// ChunkType is a type declaration (struct, interface, etc.).
	ChunkType ChunkKind = "type"
	// ChunkConst is a const block or single const declaration.
	ChunkConst ChunkKind = "const"
)

// Chunk is an indexable unit of source code extracted from a file.
type Chunk struct {
	FilePath  string
	Name      string
	Kind      ChunkKind
	StartLine int
	EndLine   int
	Content   string
}

// ChunkGoSource parses Go source code and extracts indexable chunks.
// Pure function: takes a file path (for metadata) and source text, returns chunks.
func ChunkGoSource(filePath, src string) ([]Chunk, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("codesearch: parse error: %w", err)
	}

	lines := strings.Split(src, "\n")
	var chunks []Chunk

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			chunks = append(chunks, chunkFromFunc(fset, lines, filePath, d))
		case *ast.GenDecl:
			chunks = append(chunks, chunksFromGenDecl(fset, lines, filePath, d)...)
		}
	}

	return chunks, nil
}

// chunkFromFunc extracts a Chunk from a function or method declaration.
func chunkFromFunc(fset *token.FileSet, lines []string, filePath string, fn *ast.FuncDecl) Chunk {
	startLine := fset.Position(fn.Pos()).Line
	endLine := fset.Position(fn.End()).Line

	kind := ChunkFunc
	name := fn.Name.Name

	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		kind = ChunkMethod
		recvType := baseTypeName(fn.Recv.List[0].Type)
		name = recvType + "." + fn.Name.Name
	}

	return Chunk{
		FilePath:  filePath,
		Name:      name,
		Kind:      kind,
		StartLine: startLine,
		EndLine:   endLine,
		Content:   extractLines(lines, startLine, endLine),
	}
}

// chunksFromGenDecl extracts chunks from a generic declaration (type, const).
func chunksFromGenDecl(fset *token.FileSet, lines []string, filePath string, gd *ast.GenDecl) []Chunk {
	switch gd.Tok {
	case token.TYPE:
		return chunksFromTypeDecl(fset, lines, filePath, gd)
	case token.CONST:
		return chunksFromConstDecl(fset, lines, filePath, gd)
	default:
		return nil
	}
}

// chunksFromTypeDecl extracts one Chunk per type spec in a declaration.
func chunksFromTypeDecl(fset *token.FileSet, lines []string, filePath string, gd *ast.GenDecl) []Chunk {
	var chunks []Chunk
	for _, spec := range gd.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok {
			continue
		}
		startLine := fset.Position(ts.Pos()).Line
		endLine := fset.Position(ts.End()).Line

		chunks = append(chunks, Chunk{
			FilePath:  filePath,
			Name:      ts.Name.Name,
			Kind:      ChunkType,
			StartLine: startLine,
			EndLine:   endLine,
			Content:   extractLines(lines, startLine, endLine),
		})
	}
	return chunks
}

// chunksFromConstDecl extracts a single Chunk for the entire const block.
func chunksFromConstDecl(fset *token.FileSet, lines []string, filePath string, gd *ast.GenDecl) []Chunk {
	if len(gd.Specs) == 0 {
		return nil
	}

	startLine := fset.Position(gd.Pos()).Line
	endLine := fset.Position(gd.End()).Line

	// Build a name from the const identifiers (up to 3 for readability).
	var names []string
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, n := range vs.Names {
			names = append(names, n.Name)
		}
	}

	name := "const"
	if len(names) > 0 {
		if len(names) <= 3 {
			name = "const(" + strings.Join(names, ",") + ")"
		} else {
			name = fmt.Sprintf("const(%s,...+%d)", strings.Join(names[:3], ","), len(names)-3)
		}
	}

	return []Chunk{{
		FilePath:  filePath,
		Name:      name,
		Kind:      ChunkConst,
		StartLine: startLine,
		EndLine:   endLine,
		Content:   extractLines(lines, startLine, endLine),
	}}
}

// extractLines returns the source lines from startLine to endLine (1-indexed, inclusive).
func extractLines(lines []string, startLine, endLine int) string {
	if startLine < 1 {
		startLine = 1
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if startLine > endLine {
		return ""
	}
	return strings.Join(lines[startLine-1:endLine], "\n")
}
