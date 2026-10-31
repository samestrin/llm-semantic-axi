package treesitter

import (
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// JavaChunker uses tree-sitter to parse Java source code.
type JavaChunker struct {
	lang *gotreesitter.Language
}

func init() {
	Register(func() semantic.Chunker { return NewJavaChunker() })
}

// NewJavaChunker creates a new tree-sitter based Java chunker.
func NewJavaChunker() *JavaChunker {
	return &JavaChunker{
		lang: grammars.JavaLanguage(),
	}
}

func (c *JavaChunker) SupportedExtensions() []string {
	return []string{"java"}
}

func (c *JavaChunker) Chunk(path string, content []byte) ([]semantic.Chunk, error) {
	tree, err := ParseSource(content, c.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Release()

	var chunks []semantic.Chunk
	c.walkNode(tree.RootNode(), content, path, &chunks, false)
	return chunks, TreeError(tree)
}

// walkNode emits a chunk for each type and method declaration it reaches.
// inType is true once the walk is inside a type body, which is what makes a
// method_declaration a method rather than a function.
func (c *JavaChunker) walkNode(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk, inType bool) {
	WalkNamedChildren(node, c.lang, func(child *gotreesitter.Node, nodeType string) {
		switch nodeType {
		case "class_declaration", "enum_declaration", "record_declaration":
			c.extractType(child, content, path, chunks, semantic.ChunkStruct)
		case "interface_declaration", "annotation_type_declaration":
			c.extractType(child, content, path, chunks, semantic.ChunkInterface)
		case "method_declaration", "constructor_declaration", "compact_constructor_declaration":
			c.extractMethod(child, content, path, chunks)
		case "enum_body_declarations":
			// Methods of an enum sit one level below enum_body.
			c.walkNode(child, content, path, chunks, inType)
		}
	})
}

func (c *JavaChunker) extractType(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk, chunkType semantic.ChunkType) {
	name := GetNodeText(GetChildByFieldName(node, "name", c.lang), content)
	if name == "" {
		return
	}

	sig := c.signature(node, content)
	*chunks = append(*chunks, NodeToChunk(node, content, path, "java", chunkType, name, sig))

	if body := GetChildByFieldName(node, "body", c.lang); body != nil {
		c.walkNode(body, content, path, chunks, true)
	}
}

func (c *JavaChunker) extractMethod(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk) {
	name := GetNodeText(GetChildByFieldName(node, "name", c.lang), content)
	if name == "" {
		return
	}

	sig := c.signature(node, content)
	*chunks = append(*chunks, NodeToChunk(node, content, path, "java", semantic.ChunkMethod, name, sig))
}

// signature is the declaration's first line with leading annotations removed.
// ExtractSignature alone would return "@Override" for an annotated method,
// which says nothing about the method.
func (c *JavaChunker) signature(node *gotreesitter.Node, content []byte) string {
	start := node.StartByte()
	for i := 0; i < node.NamedChildCount(); i++ {
		mods := node.NamedChild(i)
		if mods.Type(c.lang) != "modifiers" {
			continue
		}
		for j := 0; j < mods.NamedChildCount(); j++ {
			ann := mods.NamedChild(j)
			if t := ann.Type(c.lang); (t == "annotation" || t == "marker_annotation") && ann.EndByte() > start {
				start = ann.EndByte()
			}
		}
		break
	}
	text := strings.TrimSpace(string(content[start:node.EndByte()]))
	if idx := strings.Index(text, "\n"); idx >= 0 {
		text = text[:idx]
	}
	return strings.TrimRight(text, " {")
}
