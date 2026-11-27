package semantic

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// Ensure GoChunker implements RefExtractor
var _ RefExtractor = (*GoChunker)(nil)

// ExtractRefs extracts function calls, imports, and type references from Go source code.
func (c *GoChunker) ExtractRefs(path string, content []byte, chunks []Chunk) ([]ChunkRef, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, content, 0)
	if err != nil {
		return nil, err
	}

	// Build a set of chunk IDs by name for quick lookup
	chunkByName := make(map[string]string, len(chunks))
	for _, ch := range chunks {
		chunkByName[ch.Name] = ch.ID
	}

	var refs []ChunkRef

	// Extract imports
	for _, imp := range file.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		// Use the last segment as the ref name (e.g., "fmt" from "fmt", "http" from "net/http")
		parts := strings.Split(importPath, "/")
		refName := parts[len(parts)-1]
		if imp.Name != nil && imp.Name.Name != "." && imp.Name.Name != "_" {
			refName = imp.Name.Name
		}
		// Associate import with all chunks in this file
		for _, ch := range chunks {
			refs = append(refs, ChunkRef{
				ChunkID: ch.ID,
				RefType: RefImports,
				RefName: refName,
			})
			break // Only associate with first chunk to avoid bloat
		}
	}

	// receivers names each method's receiver by the line its func keyword is
	// on, which is the chunk's StartLine, so a call through it can be told
	// apart from a call through any other value.
	receivers := map[int]string{}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv != nil && len(fn.Recv.List) > 0 && len(fn.Recv.List[0].Names) > 0 {
			receivers[fset.Position(fn.Pos()).Line] = fn.Recv.List[0].Names[0].Name
		}
	}

	// Walk AST for function calls and type references per chunk
	for _, ch := range chunks {
		startLine := ch.StartLine
		endLine := ch.EndLine

		ast.Inspect(file, func(n ast.Node) bool {
			if n == nil {
				return false
			}

			pos := fset.Position(n.Pos())
			if pos.Line < startLine || pos.Line > endLine {
				return true // outside this chunk, keep walking
			}

			switch expr := n.(type) {
			case *ast.CallExpr:
				callName := extractCallName(expr)
				if callName != "" && !isGoRecursion(ch, callName, receivers[ch.StartLine]) {
					refs = append(refs, ChunkRef{
						ChunkID: ch.ID,
						RefType: RefCalls,
						RefName: callName,
					})
				}

			case *ast.SelectorExpr:
				// Type references like pkg.Type
				if ident, ok := expr.X.(*ast.Ident); ok {
					typeName := ident.Name + "." + expr.Sel.Name
					refs = append(refs, ChunkRef{
						ChunkID: ch.ID,
						RefType: RefUsesType,
						RefName: typeName,
					})
				}
			}

			return true
		})
	}

	return refs, nil
}

// isGoRecursion reports whether a call named callName, made from chunk ch, is
// ch calling itself, which writes no edge. It is the rule the tree-sitter
// extractors follow: f() inside function f is recursion, and so is recv.f()
// inside method f when recv is the method's own receiver. A bare f() inside
// method f is not, since Go never calls a method by its bare name: it calls
// the package-level f and keeps its edge.
func isGoRecursion(ch Chunk, callName, receiver string) bool {
	if ch.Type == ChunkMethod {
		return receiver != "" && callName == receiver+"."+ch.Name
	}
	return callName == ch.Name
}

// extractCallName extracts the function/method name from a call expression.
func extractCallName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if ident, ok := fn.X.(*ast.Ident); ok {
			return ident.Name + "." + fn.Sel.Name
		}
		return fn.Sel.Name
	}
	return ""
}
