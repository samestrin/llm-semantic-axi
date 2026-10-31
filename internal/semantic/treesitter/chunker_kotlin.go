package treesitter

import (
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// KotlinChunker uses tree-sitter to parse Kotlin source and scripts.
//
// The Kotlin grammar has no `name` or `body` fields, so names and bodies are
// found by child node type. It also misparses two common shapes at file
// scope, both handled in walk: a bare `object Name { ... }` comes back as an
// infix expression, and an annotation with arguments comes back detached from
// the declaration below it.
type KotlinChunker struct {
	lang *gotreesitter.Language
}

func init() {
	Register(func() semantic.Chunker { return NewKotlinChunker() })
}

// NewKotlinChunker creates a new tree-sitter based Kotlin chunker.
func NewKotlinChunker() *KotlinChunker {
	return &KotlinChunker{lang: grammars.KotlinLanguage()}
}

func (c *KotlinChunker) SupportedExtensions() []string {
	return []string{"kt", "kts"}
}

func (c *KotlinChunker) Chunk(path string, content []byte) ([]semantic.Chunk, error) {
	tree, err := ParseSource(content, c.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Release()

	var chunks []semantic.Chunk
	c.walk(tree.RootNode(), content, path, &chunks, "")
	return chunks, TreeError(tree)
}

// walk visits the declarations of a scope: the file or a type body. owner is
// the enclosing type's name, empty at file scope; it makes a function a method
// and names a secondary constructor.
func (c *KotlinChunker) walk(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk, owner string) {
	// lead is the first of a run of detached annotations, and last the most
	// recent node of that run. A declaration that follows the run starts at lead.
	var lead, last *gotreesitter.Node
	WalkNamedChildren(node, c.lang, func(child *gotreesitter.Node, nodeType string) {
		switch {
		case nodeType == "annotation":
			if lead == nil {
				lead = child
			}
			last = child
			return
		case nodeType == "parenthesized_expression" && last != nil &&
			last.Type(c.lang) == "annotation" && child.StartByte() == last.EndByte():
			// The arguments of the annotation just before it: `@Name("x")`.
			last = child
			return
		}
		outer := child
		if lead != nil {
			outer = lead
		}
		lead, last = nil, nil

		switch nodeType {
		case "class_declaration", "object_declaration", "companion_object":
			c.extractType(child, outer, content, path, chunks)
		case "function_declaration":
			c.extractFunction(child, outer, content, path, chunks, owner != "")
		case "secondary_constructor":
			if owner != "" {
				*chunks = append(*chunks, c.chunk(child, outer, content, path, semantic.ChunkMethod, owner))
			}
		case "infix_expression":
			c.extractBareObject(child, outer, content, path, chunks)
		}
	})
}

// extractType chunks a class, interface, enum class, object or companion
// object, then the members of its body.
func (c *KotlinChunker) extractType(node, outer *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk) {
	name := GetNodeText(firstChildOfType(node, c.lang, "type_identifier"), content)
	if name == "" && node.Type(c.lang) == "companion_object" {
		// An unnamed companion is called Companion, which is how callers refer to it.
		name = "Companion"
	}
	if name == "" {
		return
	}

	chunkType := semantic.ChunkStruct
	for _, child := range node.Children() {
		if !child.IsNamed() && child.Type(c.lang) == "interface" {
			chunkType = semantic.ChunkInterface
			break
		}
	}
	*chunks = append(*chunks, c.chunk(node, outer, content, path, chunkType, name))

	for _, body := range []string{"class_body", "enum_class_body"} {
		if b := firstChildOfType(node, c.lang, body); b != nil {
			c.walk(b, content, path, chunks, name)
		}
	}
}

func (c *KotlinChunker) extractFunction(node, outer *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk, inType bool) {
	name := GetNodeText(firstChildOfType(node, c.lang, "simple_identifier"), content)
	if name == "" {
		return
	}
	chunkType := semantic.ChunkFunction
	if inType {
		chunkType = semantic.ChunkMethod
	}
	*chunks = append(*chunks, c.chunk(node, outer, content, path, chunkType, name))
}

// extractBareObject chunks the infix expression the grammar produces for a
// file-scope `object Name { ... }` with no modifiers or supertypes: the
// children are exactly `object`, the name, and the body as a lambda. Any other
// infix expression is ordinary code and is left alone.
func (c *KotlinChunker) extractBareObject(node, outer *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk) {
	if node.NamedChildCount() != 3 {
		return
	}
	kw, id, body := node.NamedChild(0), node.NamedChild(1), node.NamedChild(2)
	if kw.Type(c.lang) != "object_literal" || id.Type(c.lang) != "simple_identifier" ||
		body.Type(c.lang) != "lambda_literal" {
		return
	}
	name := GetNodeText(id, content)
	*chunks = append(*chunks, c.chunk(node, outer, content, path, semantic.ChunkStruct, name))
	if stmts := firstChildOfType(body, c.lang, "statements"); stmts != nil {
		c.walk(stmts, content, path, chunks, name)
	}
}

// chunk builds the chunk for node, starting at outer when detached
// annotations precede it.
func (c *KotlinChunker) chunk(node, outer *gotreesitter.Node, content []byte, path string, chunkType semantic.ChunkType, name string) semantic.Chunk {
	ch := NodeToChunk(node, content, path, "kotlin", chunkType, name, c.signature(node, content))
	if outer.StartByte() < node.StartByte() {
		ch.Content = TruncateContent(string(content[outer.StartByte():node.EndByte()]), maxChunkSize)
		ch.StartLine = int(outer.StartPoint().Row) + 1
		ch.ID = ch.GenerateID()
	}
	return ch
}

// signature is the declaration's first line with leading annotations removed.
// ExtractSignature alone would return "@JvmStatic" for an annotated function,
// which says nothing about the function.
func (c *KotlinChunker) signature(node *gotreesitter.Node, content []byte) string {
	start := node.StartByte()
	if mods := firstChildOfType(node, c.lang, "modifiers"); mods != nil {
		WalkNamedChildren(mods, c.lang, func(m *gotreesitter.Node, nodeType string) {
			if nodeType == "annotation" && m.EndByte() > start {
				start = m.EndByte()
			}
		})
	}
	text := strings.TrimSpace(string(content[start:node.EndByte()]))
	if idx := strings.Index(text, "\n"); idx >= 0 {
		text = text[:idx]
	}
	return strings.TrimRight(text, " {")
}
