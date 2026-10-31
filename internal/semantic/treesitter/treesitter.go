// Package treesitter provides tree-sitter-based chunkers for semantic code indexing.
// It uses the pure-Go gotreesitter runtime for accurate AST parsing of Python,
// JavaScript/TypeScript, PHP, and Rust source code.
package treesitter

import (
	"errors"
	"fmt"
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// maxChunkSize is the maximum content size for a chunk in characters.
// Chunks exceeding this are truncated with a comment indicating truncation.
const maxChunkSize = 4000

// ErrParseTree reports source the grammar could not parse cleanly: the tree
// has an error or missing node, or the parser stopped before the end.
var ErrParseTree = errors.New("parse tree has errors")

// ParseSource parses source bytes with the given language and returns the tree.
// The caller must call tree.Release() when done.
func ParseSource(content []byte, lang *gotreesitter.Language) (*gotreesitter.Tree, error) {
	parser := gotreesitter.NewParser(lang)
	return parser.Parse(content)
}

// TreeError returns ErrParseTree if the grammar could not parse the tree
// cleanly, and nil otherwise. A chunker returns its chunks together with
// TreeError(tree), so the fallback wrapper can keep what the walk found and
// window the lines it missed, rather than pass a partial file off as whole.
func TreeError(tree *gotreesitter.Tree) error {
	if tree.RootNode().HasError() || tree.ParseStoppedEarly() {
		return fmt.Errorf("%w (stop reason %v)", ErrParseTree, tree.ParseStopReason())
	}
	return nil
}

// NodeToChunk converts a tree-sitter node to a semantic.Chunk.
func NodeToChunk(node *gotreesitter.Node, content []byte, path, language string, chunkType semantic.ChunkType, name, signature string) semantic.Chunk {
	startPoint := node.StartPoint()
	endPoint := node.EndPoint()
	text := node.Text(content)

	// Truncate if too large
	text = TruncateContent(text, maxChunkSize)

	chunk := semantic.Chunk{
		FilePath:  path,
		Type:      chunkType,
		Name:      name,
		Signature: signature,
		Content:   text,
		StartLine: int(startPoint.Row) + 1, // Convert 0-based to 1-based
		EndLine:   int(endPoint.Row) + 1,
		Language:  language,
	}
	chunk.ID = chunk.GenerateID()
	return chunk
}

// ExtractSignature extracts the first line of a node's text as a signature.
func ExtractSignature(node *gotreesitter.Node, content []byte) string {
	text := node.Text(content)
	if idx := strings.Index(text, "\n"); idx >= 0 {
		return strings.TrimRight(text[:idx], " {")
	}
	return strings.TrimRight(text, " {")
}

// TruncateContent limits content to maxSize characters, splitting at the last newline.
func TruncateContent(content string, maxSize int) string {
	if len(content) <= maxSize {
		return content
	}
	truncated := content[:maxSize]
	if lastNewline := strings.LastIndex(truncated, "\n"); lastNewline > maxSize/2 {
		truncated = truncated[:lastNewline]
	}
	return truncated + "\n// ... truncated"
}

// GetChildByFieldName is a convenience wrapper for Node.ChildByFieldName.
func GetChildByFieldName(node *gotreesitter.Node, name string, lang *gotreesitter.Language) *gotreesitter.Node {
	return node.ChildByFieldName(name, lang)
}

// GetNodeText extracts the text of a node from source, returning empty string if node is nil.
func GetNodeText(node *gotreesitter.Node, content []byte) string {
	if node == nil {
		return ""
	}
	return node.Text(content)
}

// WalkNamedChildren iterates over named children of a node, calling fn for each.
func WalkNamedChildren(node *gotreesitter.Node, lang *gotreesitter.Language, fn func(child *gotreesitter.Node, nodeType string)) {
	for i := 0; i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		fn(child, child.Type(lang))
	}
}

// GetLangEntry returns the gotreesitter language entry for a file extension.
func GetLangEntry(ext string) *grammars.LangEntry {
	// grammars.DetectLanguage expects a filename, not just extension
	return grammars.DetectLanguage("file." + ext)
}

// IncludesDecorators checks for decorator nodes above a function/class definition
// and returns the full node including decorators if present.
// Returns the original node and any decorator text prefix.
func IncludesDecorators(node *gotreesitter.Node, lang *gotreesitter.Language, content []byte) (startNode *gotreesitter.Node, decoratorText string) {
	parent := node.Parent()
	if parent != nil && parent.Type(lang) == "decorated_definition" {
		// The parent includes the decorators
		// Extract decorator text from parent start to node start
		parentStart := parent.StartByte()
		nodeStart := node.StartByte()
		if parentStart < nodeStart {
			decoratorText = string(content[parentStart:nodeStart])
		}
		return parent, strings.TrimSpace(decoratorText)
	}
	return node, ""
}
