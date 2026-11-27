package treesitter

import (
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/samestrin/llm-semantic-axi/v2/internal/semantic"
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

// extractJSRefs records calls and imports. It records no uses_type: in
// JavaScript and TypeScript a member access is a value lookup, not a type
// use. Each call goes to the innermost chunk holding its line only, so a class
// does not "call" what its methods call. Recursion writes no edge, by the rule
// isRecursion states for every language. Text windows the fallback put in for
// code that did not parse get no edges, since the tree there is not reliable.
// Imports go to the first chunk, as Go's do, whatever form records them:
// import statements, re-exports, require() and dynamic import().
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
		case "import_statement", "export_statement":
			// An export_statement with a source is a re-export (`export { a }
			// from "./x"`, `export * from "./x"`); without one it exports local
			// bindings and the field lookup gives "", so no edge is recorded.
			if name := jsImportName(GetNodeText(GetChildByFieldName(n, "source", lang), content)); name != "" {
				refs = append(refs, semantic.ChunkRef{ChunkID: parsed[0].ID, RefType: semantic.RefImports, RefName: name})
			}
		case "call_expression":
			fn := GetChildByFieldName(n, "function", lang)
			if isModuleLoad(fn, lang, content) {
				// require("x") and import("./x") load a module; they are not
				// calls to a chunk named require or import. Only a static
				// module path can be named; anything else records nothing.
				if name := jsImportName(jsStaticModuleArg(n, lang, content)); name != "" {
					refs = append(refs, semantic.ChunkRef{ChunkID: parsed[0].ID, RefType: semantic.RefImports, RefName: name})
				}
				break
			}
			if name := jsCallName(fn, lang, content); name != "" {
				if id := innermostChunk(parsed, int(n.StartPoint().Row)+1); id != "" && !isRecursion(parsed, id, name) {
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

// isModuleLoad reports whether a call_expression's function node makes the
// call a module load: the `import` keyword (dynamic import) or the bare name
// `require`. A `require` reached through an object (`obj.require()`) is a
// member call, not the CommonJS loader, and stays out.
func isModuleLoad(fn *gotreesitter.Node, lang *gotreesitter.Language, content []byte) bool {
	if fn == nil {
		return false
	}
	switch fn.Type(lang) {
	case "import":
		return true
	case "identifier":
		return GetNodeText(fn, content) == "require"
	}
	return false
}

// jsStaticModuleArg returns the module path of a require() or import() call
// only when it is statically known: exactly one argument, a string literal or
// a template literal with no substitution. A variable, a missing or extra
// argument, or a `${...}` hole gives "": the module cannot be named, and a
// guessed name would be an invented edge.
func jsStaticModuleArg(call *gotreesitter.Node, lang *gotreesitter.Language, content []byte) string {
	args := GetChildByFieldName(call, "arguments", lang)
	if args == nil || args.NamedChildCount() != 1 {
		return ""
	}
	arg := args.NamedChild(0)
	switch arg.Type(lang) {
	case "string":
		return GetNodeText(arg, content)
	case "template_string":
		for i := 0; i < arg.NamedChildCount(); i++ {
			if arg.NamedChild(i).Type(lang) == "template_substitution" {
				return ""
			}
		}
		return GetNodeText(arg, content)
	}
	return ""
}

// jsCallName names a callee only where the name says what is called: a bare
// name as itself, a member of `this` as "this.prop". Any other member call
// (`obj.get()`, `super.get()`, `a.b.get()`) gives "": the resolver links on
// the text after the last dot, so it would link to any lone chunk named
// "get" though nothing ties obj to it. A callee with no name (a call result,
// a computed member) gives "" too. No edge is recorded for "". Module loads
// (require, dynamic import) never reach here; extractJSRefs handles them.
func jsCallName(fn *gotreesitter.Node, lang *gotreesitter.Language, content []byte) string {
	if fn == nil {
		return ""
	}
	switch fn.Type(lang) {
	case "identifier":
		return GetNodeText(fn, content)
	case "member_expression":
		prop := GetChildByFieldName(fn, "property", lang)
		obj := GetChildByFieldName(fn, "object", lang)
		if prop == nil || obj == nil || obj.Type(lang) != "this" {
			return ""
		}
		return "this." + GetNodeText(prop, content)
	}
	return ""
}

// jsImportName turns an import source such as "./util/io.js", "./a.test" or
// "react" into its last path segment up to the first dot: "io", "a", "react".
// Chunk names are bare identifiers and the resolver matches on the text after
// the last dot, so any dotted suffix (a module extension like .js, or a
// convention like .test) would have it look for a chunk named "js" or "test".
// A leading dot is kept, so a dotfile such as "./.hidden" keeps its name.
func jsImportName(source string) string {
	source = strings.Trim(source, "\"'`")
	name := source[strings.LastIndex(source, "/")+1:]
	if i := strings.Index(name, "."); i > 0 {
		return name[:i]
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
