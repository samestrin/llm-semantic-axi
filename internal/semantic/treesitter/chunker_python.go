package treesitter

import (
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// PythonChunker uses tree-sitter to parse Python source code into semantic chunks.
type PythonChunker struct {
	lang *gotreesitter.Language
}

func init() {
	Register(func() semantic.Chunker { return NewPythonChunker() })
}

// NewPythonChunker creates a new tree-sitter based Python chunker.
func NewPythonChunker() *PythonChunker {
	return &PythonChunker{
		lang: grammars.PythonLanguage(),
	}
}

func (c *PythonChunker) SupportedExtensions() []string {
	return []string{"py", "pyw", "pyi"}
}

func (c *PythonChunker) Chunk(path string, content []byte) ([]semantic.Chunk, error) {
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

func (c *PythonChunker) walkNode(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk, enclosingClass string) {
	WalkNamedChildren(node, c.lang, func(child *gotreesitter.Node, nodeType string) {
		switch nodeType {
		case "function_definition":
			c.extractFunction(child, content, path, chunks, enclosingClass)

		case "class_definition":
			c.extractClass(child, content, path, chunks)

		case "decorated_definition":
			// Look at the definition inside the decorator
			defNode := GetChildByFieldName(child, "definition", c.lang)
			if defNode != nil {
				defType := defNode.Type(c.lang)
				switch defType {
				case "function_definition":
					c.extractFunction(child, content, path, chunks, enclosingClass)
				case "class_definition":
					c.extractClass(defNode, content, path, chunks)
				}
			}
		}
	})
}

func (c *PythonChunker) extractFunction(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk, enclosingClass string) {
	// For decorated_definition, the actual function is inside
	funcNode := node
	if node.Type(c.lang) == "decorated_definition" {
		if defNode := GetChildByFieldName(node, "definition", c.lang); defNode != nil {
			funcNode = defNode
		}
	}

	nameNode := GetChildByFieldName(funcNode, "name", c.lang)
	name := GetNodeText(nameNode, content)
	if name == "" {
		return
	}

	chunkType := semantic.ChunkFunction
	if enclosingClass != "" {
		chunkType = semantic.ChunkMethod
	}

	sig := ExtractSignature(funcNode, content)

	chunk := NodeToChunk(node, content, path, "py", chunkType, name, sig)
	*chunks = append(*chunks, chunk)
}

func (c *PythonChunker) extractClass(node *gotreesitter.Node, content []byte, path string, chunks *[]semantic.Chunk) {
	nameNode := GetChildByFieldName(node, "name", c.lang)
	className := GetNodeText(nameNode, content)
	if className == "" {
		return
	}

	sig := ExtractSignature(node, content)
	chunk := NodeToChunk(node, content, path, "py", semantic.ChunkStruct, className, sig)
	*chunks = append(*chunks, chunk)

	// Also extract methods inside the class body
	bodyNode := GetChildByFieldName(node, "body", c.lang)
	if bodyNode != nil {
		c.walkNode(bodyNode, content, path, chunks, className)
	}
}

var _ semantic.RefExtractor = (*PythonChunker)(nil)

// ExtractRefs records the calls and imports in Python source, in the shape of
// GoChunker.ExtractRefs, with two differences. There are no uses_type edges:
// a Python attribute access is not a type use. And each edge belongs to the
// innermost chunk around it, because a class chunk spans its methods and would
// otherwise "call" everything they call.
//
// A call is recorded for a bare name (helper()) and for a self. or cls.
// receiver (self.save()); other receivers name nothing the index can resolve.
// No edge is attributed to the text windows the fallback puts in place of code
// the grammar could not parse.
func (c *PythonChunker) ExtractRefs(path string, content []byte, chunks []semantic.Chunk) ([]semantic.ChunkRef, error) {
	var owners, windows []semantic.Chunk
	for _, ch := range chunks {
		if ch.Type != semantic.ChunkFile {
			owners = append(owners, ch)
		} else {
			windows = append(windows, ch)
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
	c.walkRefs(tree.RootNode(), content, owners, windows, &refs)
	return refs, nil
}

func (c *PythonChunker) walkRefs(node *gotreesitter.Node, content []byte, owners, windows []semantic.Chunk, refs *[]semantic.ChunkRef) {
	WalkNamedChildren(node, c.lang, func(child *gotreesitter.Node, nodeType string) {
		switch nodeType {
		case "call":
			id := innermostChunk(owners, nodeLine(child))
			name := c.callName(child, content)
			if id == "" {
				// A module-level call (main() under the __main__ guard)
				// belongs to the file's first chunk, as imports do. It is
				// not recursion, so the own-name filter does not apply.
				// One inside a fallback text window is code the grammar
				// could not parse, so it gets no edge.
				if name != "" && innermostChunk(windows, nodeLine(child)) == "" {
					*refs = append(*refs, semantic.ChunkRef{ChunkID: owners[0].ID, RefType: semantic.RefCalls, RefName: name})
				}
			} else if name != "" && bareName(name) != chunkName(owners, id) {
				*refs = append(*refs, semantic.ChunkRef{ChunkID: id, RefType: semantic.RefCalls, RefName: name})
			}

		case "import_statement", "import_from_statement":
			// A top-level import belongs to the file's first chunk, as in
			// GoChunker; one inside a function belongs to that function.
			id := innermostChunk(owners, nodeLine(child))
			if id == "" {
				id = owners[0].ID
			}
			for _, name := range c.importNames(child, content) {
				*refs = append(*refs, semantic.ChunkRef{ChunkID: id, RefType: semantic.RefImports, RefName: name})
			}
			return
		}
		c.walkRefs(child, content, owners, windows, refs)
	})
}

// bareName drops a self. or cls. receiver, so f() and self.f() compare equal
// to the chunk named f: recursion is one rule, whichever way it is spelled.
func bareName(name string) string {
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return name
}

// callName returns the name a call is recorded under, or "" when its target
// is not a bare name or a self./cls. method.
func (c *PythonChunker) callName(call *gotreesitter.Node, content []byte) string {
	fn := GetChildByFieldName(call, "function", c.lang)
	if fn == nil {
		return ""
	}
	switch fn.Type(c.lang) {
	case "identifier":
		return GetNodeText(fn, content)
	case "attribute":
		obj := GetChildByFieldName(fn, "object", c.lang)
		attr := GetChildByFieldName(fn, "attribute", c.lang)
		if obj == nil || attr == nil || obj.Type(c.lang) != "identifier" {
			return ""
		}
		if recv := GetNodeText(obj, content); recv == "self" || recv == "cls" {
			return recv + "." + GetNodeText(attr, content)
		}
	}
	return ""
}

// importNames returns the names an import statement brings in: the last
// segment of each imported module or name, ignoring aliases, so
// "import os.path" gives "path" and "from m import a as b" gives "a". A
// wildcard import names nothing.
func (c *PythonChunker) importNames(stmt *gotreesitter.Node, content []byte) []string {
	var names []string
	for i := 0; i < stmt.ChildCount(); i++ {
		if stmt.FieldNameForChild(i, c.lang) != "name" {
			continue
		}
		target := stmt.Child(i)
		if target.Type(c.lang) == "aliased_import" {
			target = GetChildByFieldName(target, "name", c.lang)
		}
		if target == nil || target.Type(c.lang) != "dotted_name" || target.NamedChildCount() == 0 {
			continue
		}
		if last := GetNodeText(target.NamedChild(target.NamedChildCount()-1), content); last != "" {
			names = append(names, last)
		}
	}
	return names
}

// nodeLine returns the 1-based line a node starts on.
func nodeLine(n *gotreesitter.Node) int { return int(n.StartPoint().Row) + 1 }

// chunkName returns the name of the chunk with this ID.
func chunkName(chunks []semantic.Chunk, id string) string {
	for _, ch := range chunks {
		if ch.ID == id {
			return ch.Name
		}
	}
	return ""
}
