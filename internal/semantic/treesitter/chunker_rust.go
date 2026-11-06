package treesitter

import (
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// RustChunker uses tree-sitter to parse Rust source code.
type RustChunker struct {
	lang *gotreesitter.Language
}

func init() {
	Register(func() semantic.Chunker { return NewRustChunker() })
}

// NewRustChunker creates a new tree-sitter based Rust chunker.
func NewRustChunker() *RustChunker {
	return &RustChunker{
		lang: grammars.RustLanguage(),
	}
}

func (c *RustChunker) SupportedExtensions() []string {
	return []string{"rs"}
}

func (c *RustChunker) Chunk(path string, content []byte) ([]semantic.Chunk, error) {
	tree, err := ParseSource(content, c.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Release()

	var chunks []semantic.Chunk
	root := tree.RootNode()

	c.walkNode(root, content, path, &chunks, "")
	return chunks, TreeError(tree)
}

func (c *RustChunker) walkNode(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk, enclosingType string) {
	WalkNamedChildren(node, c.lang, func(child *gotreesitter.Node, nodeType string) {
		switch nodeType {
		case "function_item":
			c.extractFunction(child, content, path, chunks, enclosingType)
		case "struct_item":
			c.extractStruct(child, content, path, chunks)
		case "enum_item":
			c.extractEnum(child, content, path, chunks)
		case "trait_item":
			c.extractTrait(child, content, path, chunks)
		case "impl_item":
			c.extractImpl(child, content, path, chunks)
		case "mod_item":
			c.extractMod(child, content, path, chunks)
		case "type_item":
			c.extractTypeAlias(child, content, path, chunks)
		}
	})
}

func (c *RustChunker) extractFunction(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk, enclosingType string) {
	nameNode := GetChildByFieldName(node, "name", c.lang)
	name := GetNodeText(nameNode, content)
	if name == "" {
		return
	}

	chunkType := semantic.ChunkFunction
	if enclosingType != "" {
		chunkType = semantic.ChunkMethod
	}

	sig := ExtractSignature(node, content)
	chunk := NodeToChunk(node, content, path, "rs", chunkType, name, sig)
	*chunks = append(*chunks, chunk)
}

func (c *RustChunker) extractStruct(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk) {
	nameNode := GetChildByFieldName(node, "name", c.lang)
	name := GetNodeText(nameNode, content)
	if name == "" {
		return
	}

	sig := ExtractSignature(node, content)
	chunk := NodeToChunk(node, content, path, "rs", semantic.ChunkStruct, name, sig)
	*chunks = append(*chunks, chunk)
}

func (c *RustChunker) extractEnum(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk) {
	nameNode := GetChildByFieldName(node, "name", c.lang)
	name := GetNodeText(nameNode, content)
	if name == "" {
		return
	}

	sig := ExtractSignature(node, content)
	chunk := NodeToChunk(node, content, path, "rs", semantic.ChunkStruct, name, sig)
	*chunks = append(*chunks, chunk)
}

func (c *RustChunker) extractTrait(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk) {
	nameNode := GetChildByFieldName(node, "name", c.lang)
	name := GetNodeText(nameNode, content)
	if name == "" {
		return
	}

	sig := ExtractSignature(node, content)
	chunk := NodeToChunk(node, content, path, "rs", semantic.ChunkInterface, name, sig)
	*chunks = append(*chunks, chunk)

	// Extract methods inside trait
	bodyNode := GetChildByFieldName(node, "body", c.lang)
	if bodyNode != nil {
		c.walkNode(bodyNode, content, path, chunks, name)
	}
}

func (c *RustChunker) extractImpl(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk) {
	// impl blocks: extract the type name and optional trait
	typeNode := GetChildByFieldName(node, "type", c.lang)
	typeName := GetNodeText(typeNode, content)
	if typeName == "" {
		return
	}

	// Check for trait: impl Trait for Type
	traitNode := GetChildByFieldName(node, "trait", c.lang)
	traitName := GetNodeText(traitNode, content)

	implName := typeName
	if traitName != "" {
		implName = typeName + "_" + traitName
	}

	sig := ExtractSignature(node, content)
	chunk := NodeToChunk(node, content, path, "rs", semantic.ChunkStruct, implName, sig)
	*chunks = append(*chunks, chunk)

	// Extract methods inside impl
	bodyNode := GetChildByFieldName(node, "body", c.lang)
	if bodyNode != nil {
		c.walkNode(bodyNode, content, path, chunks, typeName)
	}
}

func (c *RustChunker) extractMod(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk) {
	nameNode := GetChildByFieldName(node, "name", c.lang)
	name := GetNodeText(nameNode, content)
	if name == "" {
		return
	}

	// Only extract modules with a body (inline modules), not declarations
	bodyNode := GetChildByFieldName(node, "body", c.lang)
	if bodyNode == nil {
		return
	}

	sig := ExtractSignature(node, content)
	chunk := NodeToChunk(node, content, path, "rs", semantic.ChunkFile, name, sig)
	*chunks = append(*chunks, chunk)
}

func (c *RustChunker) extractTypeAlias(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk) {
	nameNode := GetChildByFieldName(node, "name", c.lang)
	name := GetNodeText(nameNode, content)
	if name == "" {
		return
	}

	sig := ExtractSignature(node, content)
	chunk := NodeToChunk(node, content, path, "rs", semantic.ChunkStruct, name, sig)
	*chunks = append(*chunks, chunk)
}

var _ semantic.RefExtractor = (*RustChunker)(nil)

// ExtractRefs records the calls and use imports in Rust source, in the shape
// of PythonChunker.ExtractRefs. There are no uses_type edges: a Rust path or
// field access is not a type use.
//
// A call belongs to the innermost chunk around it, and only when that chunk is
// a function or method: an impl spans its methods and would otherwise "call"
// everything they call, and a call in an impl const has no function to belong
// to. A call no function holds (a static or const initialiser, the body of an
// inline mod, a fallback text window) writes no edge. A call is recorded for a
// bare name (helper(), helper::<T>()) and for a self. receiver (self.save())
// whose method the caller's own impl or trait defines. Other receivers
// (obj.get()), path calls (Foo::new(), Self::f()) and macros write no edge:
// the resolver links on the last name alone, so they would link to whatever
// lone chunk shares that name.
//
// A use inside a function belongs to it; a top-level one to the file's first
// chunk, as in GoChunker. A use inside an inline mod or a text window (both
// ChunkFile chunks) writes no edge.
func (c *RustChunker) ExtractRefs(path string, content []byte, chunks []semantic.Chunk) ([]semantic.ChunkRef, error) {
	var owners, files []semantic.Chunk
	for _, ch := range chunks {
		if ch.Type != semantic.ChunkFile {
			owners = append(owners, ch)
		} else {
			files = append(files, ch)
		}
	}
	if len(owners) == 0 {
		return nil, nil
	}

	tree, err := ParseSource(content, c.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Release()

	var refs []semantic.ChunkRef
	c.walkRefs(tree.RootNode(), content, owners, files, &refs)
	return refs, nil
}

func (c *RustChunker) walkRefs(node *gotreesitter.Node, content []byte, owners, files []semantic.Chunk, refs *[]semantic.ChunkRef) {
	WalkNamedChildren(node, c.lang, func(child *gotreesitter.Node, nodeType string) {
		switch nodeType {
		case "call_expression":
			id := innermostChunk(owners, nodeLine(child))
			name := c.callName(child, content)
			if strings.HasPrefix(name, "self.") && !containerHasMethod(owners, id, bareName(name)) {
				name = ""
			}
			if name != "" && isFunctionChunk(owners, id) && bareName(name) != chunkName(owners, id) {
				*refs = append(*refs, semantic.ChunkRef{ChunkID: id, RefType: semantic.RefCalls, RefName: name})
			}

		case "use_declaration":
			id := innermostChunk(owners, nodeLine(child))
			if id == "" {
				if innermostChunk(files, nodeLine(child)) != "" {
					return
				}
				id = owners[0].ID
			}
			for _, name := range c.useNames(GetChildByFieldName(child, "argument", c.lang), content) {
				*refs = append(*refs, semantic.ChunkRef{ChunkID: id, RefType: semantic.RefImports, RefName: name})
			}
			return
		}
		c.walkRefs(child, content, owners, files, refs)
	})
}

// isFunctionChunk reports whether the chunk with this ID is a function or
// method.
func isFunctionChunk(chunks []semantic.Chunk, id string) bool {
	for _, ch := range chunks {
		if ch.ID == id {
			return ch.Type == semantic.ChunkFunction || ch.Type == semantic.ChunkMethod
		}
	}
	return false
}

// containerHasMethod reports whether the impl or trait chunk around the method
// with this ID also holds a method chunk named name. The resolver links
// self.name to any lone rs chunk called name, so a self. call is only worth
// recording when the caller's own container defines it. A free function has
// no container and so no self. call.
func containerHasMethod(chunks []semantic.Chunk, id, name string) bool {
	var caller *semantic.Chunk
	for i := range chunks {
		if chunks[i].ID == id {
			caller = &chunks[i]
			break
		}
	}
	if caller == nil || caller.Type != semantic.ChunkMethod {
		return false
	}
	var container *semantic.Chunk
	for i := range chunks {
		ch := &chunks[i]
		if ch.Type != semantic.ChunkStruct && ch.Type != semantic.ChunkInterface {
			continue
		}
		if ch.StartLine > caller.StartLine || ch.EndLine < caller.EndLine {
			continue
		}
		if container == nil || ch.EndLine-ch.StartLine < container.EndLine-container.StartLine {
			container = ch
		}
	}
	if container == nil {
		return false
	}
	for _, ch := range chunks {
		if ch.Type == semantic.ChunkMethod && ch.Name == name &&
			ch.StartLine >= container.StartLine && ch.EndLine <= container.EndLine {
			return true
		}
	}
	return false
}

// callName returns the name a call is recorded under, or "" when its target
// is not a bare name or a self. method.
func (c *RustChunker) callName(call *gotreesitter.Node, content []byte) string {
	fn := GetChildByFieldName(call, "function", c.lang)
	if fn != nil && fn.Type(c.lang) == "generic_function" {
		fn = GetChildByFieldName(fn, "function", c.lang)
	}
	if fn == nil {
		return ""
	}
	switch fn.Type(c.lang) {
	case "identifier":
		return GetNodeText(fn, content)
	case "field_expression":
		value := GetChildByFieldName(fn, "value", c.lang)
		field := GetChildByFieldName(fn, "field", c.lang)
		if value != nil && field != nil && value.Type(c.lang) == "self" {
			return "self." + GetNodeText(field, content)
		}
	}
	return ""
}

// useNames returns the names a use tree brings in: the last segment of each
// path, ignoring aliases, so "use a::b as c" gives "b" and
// "use a::{b, c::d}" gives "b" and "d". A self item names its path's last
// segment ("use a::{self}" gives "a"). A wildcard names nothing.
func (c *RustChunker) useNames(n *gotreesitter.Node, content []byte) []string {
	if n == nil {
		return nil
	}
	switch n.Type(c.lang) {
	case "identifier":
		return []string{GetNodeText(n, content)}
	case "scoped_identifier":
		if name := GetChildByFieldName(n, "name", c.lang); name != nil && name.Type(c.lang) == "identifier" {
			return []string{GetNodeText(name, content)}
		}
	case "use_as_clause":
		return c.useNames(GetChildByFieldName(n, "path", c.lang), content)
	case "use_list":
		var names []string
		for i := 0; i < n.NamedChildCount(); i++ {
			names = append(names, c.useNames(n.NamedChild(i), content)...)
		}
		return names
	case "scoped_use_list":
		list := GetChildByFieldName(n, "list", c.lang)
		if list == nil {
			return nil
		}
		var names []string
		for i := 0; i < list.NamedChildCount(); i++ {
			item := list.NamedChild(i)
			if item.Type(c.lang) == "self" {
				names = append(names, c.useNames(GetChildByFieldName(n, "path", c.lang), content)...)
				continue
			}
			names = append(names, c.useNames(item, content)...)
		}
		return names
	}
	return nil
}
