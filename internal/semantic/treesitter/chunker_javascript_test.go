package treesitter

import (
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

func assertJSChunks(t *testing.T, got []semantic.Chunk, want []tsChunk) {
	t.Helper()
	assertTSChunks(t, got, want)
}

// Defends `export default class Foo` in a .js file being chunked: the walker's
// class arm must match the node both grammars produce for it, or the class and
// its methods are missing from the index while the file reads as cleanly
// parsed. The TypeScript chunker reuses this arm, so this covers .ts too.
func TestJSChunker_ChunksExportedDefaultClasses(t *testing.T) {
	for _, path := range []string{"a.js", "a.ts"} {
		src := "export default class Foo {\n  bar() {}\n}\n"
		var (
			chunks []semantic.Chunk
			err    error
		)
		if path == "a.ts" {
			chunks, err = NewTypeScriptChunker().Chunk(path, []byte(src))
		} else {
			chunks, err = NewJSChunker().Chunk(path, []byte(src))
		}
		if err != nil {
			t.Fatalf("Chunk(%s) returned %v", path, err)
		}
		assertJSChunks(t, chunks, []tsChunk{
			{"Foo", semantic.ChunkStruct, 1, 3},
			{"bar", semantic.ChunkMethod, 2, 2},
		})
	}
}

// Defends a class field holding an arrow function being indexed as a method:
// `bar = () => {}` is a callable member, and before this only the enclosing
// class was stored, so `bar` could not be searched for or called into. A field
// holding a value is not a function and must still be skipped.
func TestJSChunker_ExtractsFunctionValuedClassFieldsAsMethods(t *testing.T) {
	src := "class Foo {\n  bar = () => {\n    return 1;\n  };\n  baz = 2;\n  qux = function () {};\n}\n"
	for _, path := range []string{"a.js", "a.ts"} {
		var (
			chunks []semantic.Chunk
			err    error
		)
		if path == "a.ts" {
			chunks, err = NewTypeScriptChunker().Chunk(path, []byte(src))
		} else {
			chunks, err = NewJSChunker().Chunk(path, []byte(src))
		}
		if err != nil {
			t.Fatalf("Chunk(%s) returned %v", path, err)
		}
		assertJSChunks(t, chunks, []tsChunk{
			{"Foo", semantic.ChunkStruct, 1, 7},
			{"bar", semantic.ChunkMethod, 2, 4},
			{"qux", semantic.ChunkMethod, 6, 6},
		})
	}
}
