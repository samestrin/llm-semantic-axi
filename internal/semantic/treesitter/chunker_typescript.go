package treesitter

import (
	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// TypeScriptChunker uses tree-sitter to parse TypeScript source code. `.tsx`
// is parsed with the TSX grammar, since the plain TypeScript grammar does not
// know JSX.
type TypeScriptChunker struct {
	tsLang  *gotreesitter.Language
	tsxLang *gotreesitter.Language
	// js extracts the constructs TypeScript shares with JavaScript. Its
	// extractors take the grammar as an argument and read no fields, so the
	// zero value loads no JavaScript grammar.
	js JSChunker
}

func init() {
	Register(func() semantic.Chunker { return NewTypeScriptChunker() })
}

// NewTypeScriptChunker creates a new tree-sitter based TypeScript chunker.
func NewTypeScriptChunker() *TypeScriptChunker {
	return &TypeScriptChunker{
		tsLang:  grammars.TypescriptLanguage(),
		tsxLang: grammars.TsxLanguage(),
	}
}

func (c *TypeScriptChunker) SupportedExtensions() []string {
	return []string{"ts", "tsx", "mts", "cts"}
}

func (c *TypeScriptChunker) Chunk(path string, content []byte) ([]semantic.Chunk, error) {
	ext := semantic.LanguageFromExtension(path)
	lang := c.tsLang
	if ext == "tsx" {
		lang = c.tsxLang
	}

	tree, err := ParseSource(content, lang)
	if err != nil {
		return nil, err
	}
	defer tree.Release()

	var chunks []semantic.Chunk
	c.walkNode(tree.RootNode(), lang, content, path, ext, &chunks)
	return chunks, TreeError(tree)
}

func (c *TypeScriptChunker) walkNode(node *gotreesitter.Node, lang *gotreesitter.Language, content []byte, path, ext string, chunks *[]semantic.Chunk) {
	WalkNamedChildren(node, lang, func(child *gotreesitter.Node, nodeType string) {
		switch nodeType {
		case "function_declaration", "function_signature":
			c.js.extractFunction(child, lang, content, path, ext, chunks, "")

		case "class_declaration", "abstract_class_declaration":
			c.js.extractClass(child, lang, content, path, ext, chunks)

		case "internal_module", "module":
			// `namespace X { ... }` and `declare module "x" { ... }` hold their
			// declarations in a statement block; the name beside it is not a
			// chunk of its own.
			c.walkNode(child, lang, content, path, ext, chunks)

		case "expression_statement", "ambient_declaration":
			// A namespace parses as an expression whose value is the
			// internal_module, and an ambient module as an ambient_declaration
			// holding it. Recurse to reach the module.
			c.walkNode(child, lang, content, path, ext, chunks)

		case "statement_block":
			// Only reached inside a module body: the arms above take functions
			// and classes whole and never walk their blocks.
			c.walkNode(child, lang, content, path, ext, chunks)

		case "interface_declaration":
			c.extractNamed(child, lang, content, path, ext, chunks, semantic.ChunkInterface)

		case "type_alias_declaration", "enum_declaration":
			c.extractNamed(child, lang, content, path, ext, chunks, semantic.ChunkStruct)

		case "lexical_declaration":
			// Arrow functions and function expressions: const foo = () => {}
			c.js.extractArrowFunctions(child, lang, content, path, ext, chunks, "")

		case "export_statement":
			c.walkNode(child, lang, content, path, ext, chunks)
		}
	})
}

// extractNamed emits one chunk of the given type for a declaration whose name
// is in its `name` field.
func (c *TypeScriptChunker) extractNamed(node *gotreesitter.Node, lang *gotreesitter.Language, content []byte, path, ext string, chunks *[]semantic.Chunk, chunkType semantic.ChunkType) {
	nameNode := GetChildByFieldName(node, "name", lang)
	name := GetNodeText(nameNode, content)
	if name == "" {
		return
	}

	sig := ExtractSignature(node, content)
	chunk := NodeToChunk(node, content, path, ext, chunkType, name, sig)
	*chunks = append(*chunks, chunk)
}
