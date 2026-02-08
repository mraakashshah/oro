package codesearch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// Summarize parses a Go source file and returns a compact text summary
// containing package name, imports, function signatures (with params, return
// types, and line numbers), type declarations (structs with fields, interfaces
// with methods), and const/var blocks. Uses only go/ast from the standard
// library.
//
// Returns an error for non-Go files or files with syntax errors.
func Summarize(filePath string) (string, error) {
	if !strings.HasSuffix(filePath, ".go") {
		return "", fmt.Errorf("codesearch: not a Go file: %s", filePath)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		return "", fmt.Errorf("codesearch: parse error: %w", err)
	}

	var b strings.Builder

	fmt.Fprintf(&b, "package %s\n", file.Name.Name)
	writeImports(&b, file)
	writeValues(&b, fset, file, token.CONST, "const")
	writeValues(&b, fset, file, token.VAR, "var")
	writeTypes(&b, fset, file)
	writeFunctions(&b, fset, file)

	return b.String(), nil
}

// writeImports writes imports as a compact single line.
func writeImports(b *strings.Builder, file *ast.File) {
	if len(file.Imports) == 0 {
		return
	}
	b.WriteString("imports: ")
	for i, imp := range file.Imports {
		if i > 0 {
			b.WriteString(", ")
		}
		if imp.Name != nil && imp.Name.Name != "" {
			b.WriteString(imp.Name.Name + " ")
		}
		b.WriteString(imp.Path.Value)
	}
	b.WriteByte('\n')
}

// writeValues writes const or var declarations on a single line each.
func writeValues(b *strings.Builder, fset *token.FileSet, file *ast.File, tok token.Token, keyword string) {
	first := true
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != tok {
			continue
		}
		for _, spec := range genDecl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			line := fset.Position(vs.Pos()).Line
			typStr := ""
			if vs.Type != nil {
				typStr = " " + exprString(vs.Type)
			}
			for _, name := range vs.Names {
				if first {
					b.WriteByte('\n')
					first = false
				}
				fmt.Fprintf(b, "L%d: %s %s%s\n", line, keyword, name.Name, typStr)
			}
		}
	}
}

// writeTypes writes exported type declarations with details.
// Unexported types are counted but not listed individually.
func writeTypes(b *strings.Builder, fset *token.FileSet, file *ast.File) {
	var exported []string
	unexportedCount := 0

	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}
		for _, spec := range genDecl.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if !ast.IsExported(ts.Name.Name) {
				unexportedCount++
				continue
			}
			line := fset.Position(ts.Pos()).Line
			exported = append(exported, formatTypeSpec(ts, line))
		}
	}

	if len(exported) == 0 && unexportedCount == 0 {
		return
	}
	b.WriteByte('\n')
	for _, e := range exported {
		b.WriteString(e)
		b.WriteByte('\n')
	}
	if unexportedCount > 0 {
		fmt.Fprintf(b, "(+ %d unexported types)\n", unexportedCount)
	}
}

// formatTypeSpec dispatches to the appropriate type formatter.
func formatTypeSpec(ts *ast.TypeSpec, line int) string {
	switch t := ts.Type.(type) {
	case *ast.StructType:
		return formatStruct(ts.Name.Name, line, t)
	case *ast.InterfaceType:
		return formatInterface(ts.Name.Name, line, t)
	default:
		return fmt.Sprintf("L%d: type %s %s", line, ts.Name.Name, exprString(ts.Type))
	}
}

// formatStruct formats a struct type showing exported fields on one line.
func formatStruct(name string, line int, st *ast.StructType) string {
	nFields := countFields(st)
	if nFields == 0 {
		return fmt.Sprintf("L%d: type %s struct {}", line, name)
	}

	fields := collectExportedFields(st)
	if len(fields) == 0 {
		return fmt.Sprintf("L%d: type %s struct { /* %d unexported fields */ }", line, name, nFields)
	}

	return fmt.Sprintf("L%d: type %s struct { %s}", line, name, strings.Join(fields, ""))
}

// countFields returns the number of field declarations in a struct.
func countFields(st *ast.StructType) int {
	if st.Fields == nil {
		return 0
	}
	return len(st.Fields.List)
}

// collectExportedFields returns formatted strings for each exported field.
func collectExportedFields(st *ast.StructType) []string {
	var fields []string
	for _, field := range st.Fields.List {
		typStr := exprString(field.Type)
		if len(field.Names) == 0 {
			// Embedded field -- always include.
			fields = append(fields, typStr+"; ")
			continue
		}
		for _, n := range field.Names {
			if ast.IsExported(n.Name) {
				fields = append(fields, n.Name+" "+typStr+"; ")
			}
		}
	}
	return fields
}

// formatInterface formats an interface type with its methods on one line.
func formatInterface(name string, line int, it *ast.InterfaceType) string {
	if it.Methods == nil || len(it.Methods.List) == 0 {
		return fmt.Sprintf("L%d: type %s interface {}", line, name)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "L%d: type %s interface { ", line, name)
	for _, method := range it.Methods.List {
		if len(method.Names) > 0 {
			if ft, ok := method.Type.(*ast.FuncType); ok {
				sb.WriteString(method.Names[0].Name + funcSignature(ft) + "; ")
			}
		} else {
			sb.WriteString(exprString(method.Type) + "; ")
		}
	}
	sb.WriteByte('}')
	return sb.String()
}

// writeFunctions writes exported function/method signatures. Unexported
// functions are counted but not listed, to maximize token savings.
func writeFunctions(b *strings.Builder, fset *token.FileSet, file *ast.File) {
	var exported []string
	unexportedCount := 0

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if isUnexportedMethod(fn) || !ast.IsExported(fn.Name.Name) {
			unexportedCount++
			continue
		}

		line := fset.Position(fn.Pos()).Line
		exported = append(exported, formatFunc(fn, line))
	}

	if len(exported) == 0 && unexportedCount == 0 {
		return
	}
	b.WriteByte('\n')
	for _, e := range exported {
		b.WriteString(e)
		b.WriteByte('\n')
	}
	if unexportedCount > 0 {
		fmt.Fprintf(b, "(+ %d unexported funcs)\n", unexportedCount)
	}
}

// isUnexportedMethod returns true if the function is a method on an
// unexported receiver type.
func isUnexportedMethod(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return false
	}
	recvType := baseTypeName(fn.Recv.List[0].Type)
	return !ast.IsExported(recvType)
}

// formatFunc formats a function or method declaration.
func formatFunc(fn *ast.FuncDecl, line int) string {
	sig := funcSignature(fn.Type)
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		recv := receiverString(fn.Recv.List[0])
		return fmt.Sprintf("L%d: func (%s) %s%s", line, recv, fn.Name.Name, sig)
	}
	return fmt.Sprintf("L%d: func %s%s", line, fn.Name.Name, sig)
}

// funcSignature returns the parameter types and return types of a function,
// e.g., "(context.Context, string) (int, error)".
func funcSignature(ft *ast.FuncType) string {
	var sb strings.Builder

	sb.WriteByte('(')
	if ft.Params != nil {
		sb.WriteString(fieldListString(ft.Params))
	}
	sb.WriteByte(')')

	if ft.Results != nil && len(ft.Results.List) > 0 {
		results := fieldListString(ft.Results)
		if len(ft.Results.List) == 1 && len(ft.Results.List[0].Names) == 0 {
			sb.WriteString(" " + results)
		} else {
			sb.WriteString(" (" + results + ")")
		}
	}

	return sb.String()
}

// fieldListString formats a field list (params or results) as types only,
// omitting parameter names to minimize token usage.
func fieldListString(fl *ast.FieldList) string {
	var parts []string
	for _, field := range fl.List {
		typStr := exprString(field.Type)
		n := len(field.Names)
		if n <= 1 {
			parts = append(parts, typStr)
		} else {
			for range n {
				parts = append(parts, typStr)
			}
		}
	}
	return strings.Join(parts, ", ")
}

// baseTypeName extracts the base type name from a receiver expression,
// stripping pointer indirection. E.g., *Server -> "Server".
func baseTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// receiverString formats a method receiver, e.g., "s *Server".
func receiverString(field *ast.Field) string {
	typStr := exprString(field.Type)
	if len(field.Names) > 0 {
		return field.Names[0].Name + " " + typStr
	}
	return typStr
}

// exprString converts an AST expression to a readable string.
func exprString(expr ast.Expr) string { //nolint:gocyclo // type switch inherently branchy
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return "*" + exprString(e.X)
	case *ast.SelectorExpr:
		return exprString(e.X) + "." + e.Sel.Name
	case *ast.ArrayType:
		return formatArrayType(e)
	case *ast.MapType:
		return "map[" + exprString(e.Key) + "]" + exprString(e.Value)
	case *ast.InterfaceType:
		return formatInterfaceExpr(e)
	case *ast.FuncType:
		return "func" + funcSignature(e)
	case *ast.Ellipsis:
		return "..." + exprString(e.Elt)
	case *ast.ChanType:
		return formatChanType(e)
	case *ast.BasicLit:
		return e.Value
	case *ast.ParenExpr:
		return "(" + exprString(e.X) + ")"
	default:
		return fmt.Sprintf("%T", expr)
	}
}

func formatArrayType(e *ast.ArrayType) string {
	if e.Len == nil {
		return "[]" + exprString(e.Elt)
	}
	return "[" + exprString(e.Len) + "]" + exprString(e.Elt)
}

func formatInterfaceExpr(e *ast.InterfaceType) string {
	if e.Methods == nil || len(e.Methods.List) == 0 {
		return "interface{}"
	}
	return "interface{...}"
}

func formatChanType(e *ast.ChanType) string {
	switch e.Dir {
	case ast.SEND:
		return "chan<- " + exprString(e.Value)
	case ast.RECV:
		return "<-chan " + exprString(e.Value)
	default:
		return "chan " + exprString(e.Value)
	}
}
