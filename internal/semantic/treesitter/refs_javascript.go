package treesitter

import (
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

var (
	_ semantic.RefExtractor = (*JSChunker)(nil)
	_ semantic.RefExtractor = (*TypeScriptChunker)(nil)
)

// ExtractRefs extracts call and import references from JavaScript source.
func (c *JSChunker) ExtractRefs(path string, content []byte, chunks []semantic.Chunk) ([]semantic.ChunkRef, error) {
	return extractJSRefs(c.jsLang, content, chunks)
}

// ExtractRefs extracts call and import references from TypeScript source,
// with the TSX grammar for `.tsx` as Chunk uses.
func (c *TypeScriptChunker) ExtractRefs(path string, content []byte, chunks []semantic.Chunk) ([]semantic.ChunkRef, error) {
	lang := c.tsLang
	if semantic.LanguageFromExtension(path) == "tsx" {
		lang = c.tsxLang
	}
	return extractJSRefs(lang, content, chunks)
}

// jsModuleExtensions are stripped from an import's last path segment, so
// "./io.js" is recorded as "io": the resolver matches on the text after the
// last dot, and would otherwise look for a chunk named "js".
var jsModuleExtensions = []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts"}

// extractJSRefs records calls and imports. It records no uses_type: in
// JavaScript and TypeScript a member access is a value lookup, not a type
// use. Each call goes to the innermost chunk holding its line only, so a class
// does not "call" what its methods call. Text windows the fallback put in for
// code that did not parse get no edges, since the tree there is not reliable.
// Imports go to the first chunk, as Go's do.
func extractJSRefs(lang *gotreesitter.Language, content []byte, chunks []semantic.Chunk) ([]semantic.ChunkRef, error) {
	var parsed []semantic.Chunk
	for _, ch := range chunks {
		if ch.Type != semantic.ChunkFile {
			parsed = append(parsed, ch)
		}
	}
	if len(parsed) == 0 {
		return nil, nil
	}

	tree, err := ParseSource(content, lang)
	if err != nil {
		return nil, err
	}
	defer tree.Release()

	var refs []semantic.ChunkRef
	var walk func(n *gotreesitter.Node)
	walk = func(n *gotreesitter.Node) {
		switch n.Type(lang) {
		case "import_statement":
			if name := jsImportName(GetNodeText(GetChildByFieldName(n, "source", lang), content)); name != "" {
				refs = append(refs, semantic.ChunkRef{ChunkID: parsed[0].ID, RefType: semantic.RefImports, RefName: name})
			}
		case "call_expression":
			if name := jsCallName(GetChildByFieldName(n, "function", lang), lang, content); name != "" {
				if id := innermostChunk(parsed, int(n.StartPoint().Row)+1); id != "" {
					refs = append(refs, semantic.ChunkRef{ChunkID: id, RefType: semantic.RefCalls, RefName: name})
				}
			}
		}
		for _, child := range n.Children() {
			walk(child)
		}
	}
	walk(tree.RootNode())
	return refs, nil
}

// jsCallName names a callee the way Go's refs do: a bare name as itself, a
// member of a name or `this` as "obj.prop", any other member by its property.
// A callee with no name (a call result, a computed member, a dynamic import)
// gives "", and no edge is recorded for it.
func jsCallName(fn *gotreesitter.Node, lang *gotreesitter.Language, content []byte) string {
	if fn == nil {
		return ""
	}
	switch fn.Type(lang) {
	case "identifier":
		return GetNodeText(fn, content)
	case "member_expression":
		prop := GetNodeText(GetChildByFieldName(fn, "property", lang), content)
		obj := GetChildByFieldName(fn, "object", lang)
		if prop == "" || obj == nil {
			return prop
		}
		switch obj.Type(lang) {
		case "identifier", "this", "super":
			return GetNodeText(obj, content) + "." + prop
		}
		return prop
	}
	return ""
}

// jsImportName turns an import source such as "./util/io.js" or "react" into
// its last path segment without a module extension.
func jsImportName(source string) string {
	source = strings.Trim(source, "\"'`")
	name := source[strings.LastIndex(source, "/")+1:]
	for _, ext := range jsModuleExtensions {
		if trimmed, ok := strings.CutSuffix(name, ext); ok && trimmed != "" {
			return trimmed
		}
	}
	return name
}

// innermostChunk returns the ID of the narrowest chunk holding line. A tie
// goes to the chunk later in the slice: the chunkers append a class before
// its methods, so on a one-line class the method wins.
func innermostChunk(chunks []semantic.Chunk, line int) string {
	best := -1
	for i, ch := range chunks {
		if line < ch.StartLine || line > ch.EndLine {
			continue
		}
		if best < 0 || ch.EndLine-ch.StartLine <= chunks[best].EndLine-chunks[best].StartLine {
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	return chunks[best].ID
}
