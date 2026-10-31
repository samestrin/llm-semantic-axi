package treesitter

import (
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// CppChunker uses tree-sitter to parse C++ source code. It also owns `.h`: the
// C++ grammar parses C headers, and a header it rejects falls back to text
// windows through the registry's wrapper.
type CppChunker struct {
	lang *gotreesitter.Language
}

func init() {
	Register(func() semantic.Chunker { return NewCppChunker() })
}

// NewCppChunker creates a new tree-sitter based C++ chunker.
func NewCppChunker() *CppChunker {
	return &CppChunker{lang: grammars.CppLanguage()}
}

func (c *CppChunker) SupportedExtensions() []string {
	return []string{"cpp", "cxx", "cc", "hpp", "hxx", "hh", "h"}
}

func (c *CppChunker) Chunk(path string, content []byte) ([]semantic.Chunk, error) {
	tree, err := ParseSource(content, c.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Release()

	var chunks []semantic.Chunk
	c.walk(tree.RootNode(), content, path, &chunks, "")
	return chunks, TreeError(tree)
}

// walk visits the declarations of a scope: the file, a namespace body, an
// extern "C" block, or a class body. `outer` is the node whose range a chunk
// should cover (a template_declaration wraps the class or function it
// templates), and is nil when the child is not wrapped.
func (c *CppChunker) walk(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk, enclosing string) {
	WalkNamedChildren(node, c.lang, func(child *gotreesitter.Node, nodeType string) {
		c.visit(child, child, nodeType, content, path, chunks, enclosing)
	})
}

func (c *CppChunker) visit(node, outer *gotreesitter.Node, nodeType string, content []byte, path string, chunks *[]semantic.Chunk, enclosing string) {
	switch nodeType {
	case "template_declaration":
		WalkNamedChildren(node, c.lang, func(inner *gotreesitter.Node, innerType string) {
			switch innerType {
			case "function_definition", "class_specifier", "struct_specifier", "union_specifier":
				c.visit(inner, node, innerType, content, path, chunks, enclosing)
			}
		})
	case "function_definition":
		c.extractFunction(node, outer, content, path, chunks, enclosing)
	case "class_specifier", "struct_specifier", "union_specifier":
		c.extractClass(node, outer, content, path, chunks)
	case "enum_specifier":
		c.extractNamed(node, outer, content, path, chunks, semantic.ChunkStruct, "enumerator_list")
	case "namespace_definition":
		c.extractNamespace(node, content, path, chunks)
	case "preproc_ifdef", "preproc_if", "preproc_else", "preproc_elif", "preproc_elifdef":
		// Include guards wrap a whole header: everything inside is file scope.
		c.walk(node, content, path, chunks, enclosing)
	case "linkage_specification":
		if body := firstChildOfType(node, c.lang, "declaration_list"); body != nil {
			c.walk(body, content, path, chunks, enclosing)
		}
	}
}

func (c *CppChunker) extractFunction(node, outer *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk, enclosing string) {
	name, qualified := c.declaratorName(GetChildByFieldName(node, "declarator", c.lang), content)
	if name == "" {
		return
	}
	chunkType := semantic.ChunkFunction
	if enclosing != "" || qualified {
		chunkType = semantic.ChunkMethod
	}
	*chunks = append(*chunks, NodeToChunk(outer, content, path, "cpp", chunkType, name, ExtractSignature(outer, content)))
}

// extractClass emits a class, struct or union that has a body, then walks the
// body for its methods. A forward declaration (`class Fwd;`) has no body and
// no code, so it is not a chunk.
func (c *CppChunker) extractClass(node, outer *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk) {
	body := firstChildOfType(node, c.lang, "field_declaration_list")
	if body == nil {
		return
	}
	name := GetNodeText(GetChildByFieldName(node, "name", c.lang), content)
	if name == "" {
		return
	}
	*chunks = append(*chunks, NodeToChunk(outer, content, path, "cpp", semantic.ChunkStruct, name, ExtractSignature(outer, content)))
	c.walk(body, content, path, chunks, name)
}

func (c *CppChunker) extractNamed(node, outer *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk, chunkType semantic.ChunkType, bodyType string) {
	if firstChildOfType(node, c.lang, bodyType) == nil {
		return
	}
	name := GetNodeText(GetChildByFieldName(node, "name", c.lang), content)
	if name == "" {
		return
	}
	*chunks = append(*chunks, NodeToChunk(outer, content, path, "cpp", chunkType, name, ExtractSignature(outer, content)))
}

// extractNamespace emits a namespace that has a body as a file-type chunk
// (as the Rust chunker does for `mod`) and walks into it. An anonymous
// namespace has no name to search by, so it is walked but not emitted.
func (c *CppChunker) extractNamespace(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk) {
	body := firstChildOfType(node, c.lang, "declaration_list")
	if body == nil {
		return
	}
	if nameNode := GetChildByFieldName(node, "name", c.lang); nameNode != nil {
		if name := GetNodeText(nameNode, content); name != "" {
			*chunks = append(*chunks, NodeToChunk(node, content, path, "cpp", semantic.ChunkFile, name, ExtractSignature(node, content)))
		}
	}
	c.walk(body, content, path, chunks, "")
}

// declaratorName digs the callable's own name out of a declarator, through
// pointer, reference and parenthesised wrappers. It reports whether the name
// was qualified (`Foo::bar`), which marks an out-of-line method definition.
func (c *CppChunker) declaratorName(d *gotreesitter.Node, content []byte) (name string, qualified bool) {
	for d != nil {
		switch d.Type(c.lang) {
		case "function_declarator", "pointer_declarator", "reference_declarator", "parenthesized_declarator":
			next := GetChildByFieldName(d, "declarator", c.lang)
			if next == nil && d.NamedChildCount() > 0 {
				next = d.NamedChild(0)
			}
			d = next
		default:
			text := GetNodeText(d, content)
			if i := strings.LastIndex(text, "::"); i >= 0 {
				return text[i+2:], true
			}
			return text, false
		}
	}
	return "", false
}

func firstChildOfType(node *gotreesitter.Node, lang *gotreesitter.Language, want string) *gotreesitter.Node {
	var found *gotreesitter.Node
	WalkNamedChildren(node, lang, func(child *gotreesitter.Node, nodeType string) {
		if found == nil && nodeType == want {
			found = child
		}
	})
	return found
}
