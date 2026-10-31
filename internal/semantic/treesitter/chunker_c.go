package treesitter

import (
	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// CChunker uses tree-sitter to parse C source code. It owns only the "c"
// extension: ".h" is ambiguous between C and C++ and is claimed by the C++
// chunker.
type CChunker struct {
	lang *gotreesitter.Language
}

func init() {
	Register(func() semantic.Chunker { return NewCChunker() })
}

// NewCChunker creates a new tree-sitter based C chunker.
func NewCChunker() *CChunker {
	return &CChunker{lang: grammars.CLanguage()}
}

func (c *CChunker) SupportedExtensions() []string {
	return []string{"c"}
}

func (c *CChunker) Chunk(path string, content []byte) ([]semantic.Chunk, error) {
	tree, err := ParseSource(content, c.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Release()

	var chunks []semantic.Chunk
	c.walkNode(tree.RootNode(), content, path, &chunks)
	return chunks, TreeError(tree)
}

func (c *CChunker) walkNode(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk) {
	WalkNamedChildren(node, c.lang, func(child *gotreesitter.Node, nodeType string) {
		switch nodeType {
		case "function_definition":
			name := c.declaratorName(GetChildByFieldName(child, "declarator", c.lang), content)
			c.add(child, content, path, semantic.ChunkFunction, name, chunks)
		case "struct_specifier", "union_specifier", "enum_specifier":
			// A tag without a body is a forward reference, not a definition.
			if GetChildByFieldName(child, "body", c.lang) == nil {
				return
			}
			name := GetNodeText(GetChildByFieldName(child, "name", c.lang), content)
			c.add(child, content, path, semantic.ChunkStruct, name, chunks)
		case "type_definition":
			name := c.declaratorName(GetChildByFieldName(child, "declarator", c.lang), content)
			c.add(child, content, path, semantic.ChunkStruct, name, chunks)
		case "preproc_if", "preproc_ifdef", "preproc_else", "preproc_elif", "linkage_specification":
			// Definitions guarded by #ifdef are still definitions.
			c.walkNode(child, content, path, chunks)
		}
	})
}

func (c *CChunker) add(node *gotreesitter.Node, content []byte, path string, t semantic.ChunkType, name string, chunks *[]semantic.Chunk) {
	if name == "" {
		return
	}
	*chunks = append(*chunks, NodeToChunk(node, content, path, "c", t, name, ExtractSignature(node, content)))
}

// declaratorName unwraps pointer, array, function and parenthesized declarators
// down to the identifier being declared.
func (c *CChunker) declaratorName(node *gotreesitter.Node, content []byte) string {
	for node != nil {
		switch node.Type(c.lang) {
		case "identifier", "type_identifier":
			return GetNodeText(node, content)
		}
		next := GetChildByFieldName(node, "declarator", c.lang)
		if next == nil && node.NamedChildCount() > 0 {
			next = node.NamedChild(0)
		}
		node = next
	}
	return ""
}
