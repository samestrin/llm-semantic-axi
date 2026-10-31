package treesitter

import (
	"fmt"
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// CSharpChunker uses tree-sitter to parse C# source code.
type CSharpChunker struct {
	lang *gotreesitter.Language
}

func init() {
	Register(func() semantic.Chunker { return NewCSharpChunker() })
}

// NewCSharpChunker creates a new tree-sitter based C# chunker.
func NewCSharpChunker() *CSharpChunker {
	return &CSharpChunker{
		lang: grammars.CSharpLanguage(),
	}
}

func (c *CSharpChunker) SupportedExtensions() []string {
	return []string{"cs"}
}

func (c *CSharpChunker) Chunk(path string, content []byte) ([]semantic.Chunk, error) {
	tree, err := ParseSource(content, c.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Release()

	var chunks []semantic.Chunk
	root := tree.RootNode()
	leaked := c.walkNode(root, root, content, path, &chunks)
	if err := TreeError(tree); err != nil {
		return chunks, err
	}
	if leaked != "" {
		// gotreesitter can recover an unclosed declaration into a tree with no
		// ERROR node that has dropped earlier declarations, leaving one of the
		// grammar's hidden (underscore) rules in their place. That tree is not
		// a clean parse, whatever HasError says.
		return chunks, fmt.Errorf("%w (hidden node %s in tree)", ErrParseTree, leaked)
	}
	return chunks, nil
}

// walkNode emits a chunk for each namespace, type and method declaration it
// reaches. It returns the type of any hidden grammar node it met, which marks
// a tree the parser silently got wrong.
func (c *CSharpChunker) walkNode(node, root *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk) string {
	var leaked string
	WalkNamedChildren(node, c.lang, func(child *gotreesitter.Node, nodeType string) {
		var inner string
		switch nodeType {
		case "namespace_declaration":
			inner = c.extract(child, content, path, chunks, semantic.ChunkFile)
		case "file_scoped_namespace_declaration":
			c.extractFileScopedNamespace(child, root, content, path, chunks)
		case "class_declaration", "struct_declaration", "record_declaration", "record_struct_declaration", "enum_declaration":
			inner = c.extract(child, content, path, chunks, semantic.ChunkStruct)
		case "interface_declaration":
			inner = c.extract(child, content, path, chunks, semantic.ChunkInterface)
		case "method_declaration", "constructor_declaration", "destructor_declaration":
			c.extract(child, content, path, chunks, semantic.ChunkMethod)
		default:
			if strings.HasPrefix(nodeType, "_") {
				inner = nodeType
			}
		}
		if leaked == "" {
			leaked = inner
		}
	})
	return leaked
}

// extract emits a named declaration and walks its body, if it has one. A
// method's body is a block, so nothing below a method is chunked.
func (c *CSharpChunker) extract(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk, chunkType semantic.ChunkType) string {
	name := GetNodeText(GetChildByFieldName(node, "name", c.lang), content)
	if name == "" {
		return ""
	}
	*chunks = append(*chunks, NodeToChunk(node, content, path, "cs", chunkType, name, c.signature(node, content)))

	if chunkType == semantic.ChunkMethod {
		return ""
	}
	if body := firstChildOfType(node, c.lang, "declaration_list"); body != nil {
		return c.walkNode(body, nil, content, path, chunks)
	}
	return ""
}

// extractFileScopedNamespace emits `namespace X;` as a file-type chunk running
// to the end of the last declaration in the file. The node itself is the one
// line `namespace X;`, and its declarations are its siblings, which the walk
// of the root chunks on its own.
func (c *CSharpChunker) extractFileScopedNamespace(node, root *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk) {
	name := GetNodeText(GetChildByFieldName(node, "name", c.lang), content)
	if name == "" || root == nil {
		return
	}
	last := root.NamedChild(root.NamedChildCount() - 1)
	chunk := NodeToChunk(node, content, path, "cs", semantic.ChunkFile, name, c.signature(node, content))
	chunk.Content = TruncateContent(string(content[node.StartByte():last.EndByte()]), maxChunkSize)
	chunk.EndLine = int(last.EndPoint().Row) + 1
	chunk.ID = chunk.GenerateID()
	*chunks = append(*chunks, chunk)
}

// signature is the declaration's first line with leading attributes removed.
// ExtractSignature alone would return "[Serializable]" for an attributed
// class, which says nothing about the class.
func (c *CSharpChunker) signature(node *gotreesitter.Node, content []byte) string {
	start := node.StartByte()
	for i := 0; i < node.NamedChildCount(); i++ {
		attr := node.NamedChild(i)
		if attr.Type(c.lang) == "attribute_list" && attr.EndByte() > start {
			start = attr.EndByte()
		}
	}
	text := strings.TrimSpace(string(content[start:node.EndByte()]))
	if idx := strings.Index(text, "\n"); idx >= 0 {
		text = text[:idx]
	}
	return strings.TrimRight(text, " {;")
}
