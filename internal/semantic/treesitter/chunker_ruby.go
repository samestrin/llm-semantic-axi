package treesitter

import (
	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// RubyChunker uses tree-sitter to parse Ruby source code. Classes are chunked
// as structs and modules as ChunkFile, the type the Rust and C++ chunkers give
// a namespace.
type RubyChunker struct {
	lang *gotreesitter.Language
}

func init() {
	Register(func() semantic.Chunker { return NewRubyChunker() })
}

// NewRubyChunker creates a new tree-sitter based Ruby chunker.
func NewRubyChunker() *RubyChunker {
	return &RubyChunker{lang: grammars.RubyLanguage()}
}

func (c *RubyChunker) SupportedExtensions() []string {
	return []string{"rb"}
}

func (c *RubyChunker) Chunk(path string, content []byte) ([]semantic.Chunk, error) {
	tree, err := ParseSource(content, c.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Release()

	var chunks []semantic.Chunk
	c.walkNode(tree.RootNode(), content, path, &chunks, false)
	return chunks, TreeError(tree)
}

// walkNode emits a chunk for each class, module and method it reaches. inType
// is true inside a class or module body, which is what makes a def a method
// rather than a top-level function.
func (c *RubyChunker) walkNode(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk, inType bool) {
	WalkNamedChildren(node, c.lang, func(child *gotreesitter.Node, nodeType string) {
		switch nodeType {
		case "class":
			c.extractType(child, content, path, chunks, semantic.ChunkStruct)
		case "module":
			c.extractType(child, content, path, chunks, semantic.ChunkFile)
		case "method", "singleton_method":
			t := semantic.ChunkFunction
			if inType || nodeType == "singleton_method" {
				t = semantic.ChunkMethod
			}
			c.add(child, content, path, t, chunks)
		case "singleton_class", "body_statement":
			// `class << self` holds class methods; body_statement wraps every
			// class, module and singleton-class body.
			c.walkNode(child, content, path, chunks, inType)
		case "call":
			// `private def x ... end` passes the def as a call argument.
			if args := GetChildByFieldName(child, "arguments", c.lang); args != nil {
				c.walkNode(args, content, path, chunks, inType)
			}
		}
	})
}

func (c *RubyChunker) extractType(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk, t semantic.ChunkType) {
	if !c.add(node, content, path, t, chunks) {
		return
	}
	c.walkNode(node, content, path, chunks, true)
}

// add appends the node as a chunk named by its name field, and reports
// whether it did. A node without a name is dropped: a nameless row cannot be
// searched for or labelled.
func (c *RubyChunker) add(node *gotreesitter.Node, content []byte, path string, t semantic.ChunkType, chunks *[]semantic.Chunk) bool {
	name := GetNodeText(GetChildByFieldName(node, "name", c.lang), content)
	if name == "" {
		return false
	}
	*chunks = append(*chunks, NodeToChunk(node, content, path, "rb", t, name, ExtractSignature(node, content)))
	return true
}
