package treesitter

import (
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

type wantChunk struct {
	name       string
	typ        semantic.ChunkType
	start, end int
}

func assertChunks(t *testing.T, chunks []semantic.Chunk, want []wantChunk) {
	t.Helper()
	for _, w := range want {
		name := w.name
		// Match on type too: a constructor shares its class's name.
		var c semantic.Chunk
		ok := false
		for _, got := range chunks {
			if got.Name == name && got.Type == w.typ {
				c, ok = got, true
			}
		}
		if !ok {
			t.Errorf("chunk %q missing; got %v", name, chunks)
			continue
		}
		if c.Type != w.typ || c.StartLine != w.start || c.EndLine != w.end {
			t.Errorf("%s = %v lines %d-%d, want %v lines %d-%d", name, c.Type, c.StartLine, c.EndLine, w.typ, w.start, w.end)
		}
		if c.Language != "cpp" {
			t.Errorf("%s language = %q, want cpp", name, c.Language)
		}
	}
}

// Defends the T6 "done when": functions, classes, structs and namespaces get
// the right names and line ranges. A template wrapper must be inside the
// class's range, a qualified out-of-line definition must be a method, and
// members inside extern "C" and namespaces must not be skipped.
func TestCppChunker_FixtureNamesAndLineRanges(t *testing.T) {
	content := readTestFile(t, "cpp", "sample.cpp")
	chunks, err := NewCppChunker().Chunk("sample.cpp", content)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	assertChunks(t, chunks, []wantChunk{
		{"geo", semantic.ChunkFile, 3, 22},
		{"Box", semantic.ChunkStruct, 5, 13},
		{"get", semantic.ChunkMethod, 10, 10},
		{"Box", semantic.ChunkMethod, 8, 8},
		{"Point", semantic.ChunkStruct, 15, 18},
		{"Color", semantic.ChunkStruct, 20, 20},
		{"geo_helper", semantic.ChunkFunction, 24, 26},
		{"size", semantic.ChunkMethod, 28, 30},
		{"make", semantic.ChunkFunction, 32, 32},
		{"c_entry", semantic.ChunkFunction, 35, 35},
	})
}

// Defends the ".h is registered once, to C++" decision: a plain C header must
// chunk with the C++ grammar and keep every function and the struct.
func TestCppChunker_CHeaderNamedDotH(t *testing.T) {
	content := readTestFile(t, "cpp", "header.h")
	chunks, err := NewCppChunker().Chunk("header.h", content)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	assertChunks(t, chunks, []wantChunk{
		{"node", semantic.ChunkStruct, 4, 7},
		{"node_sum", semantic.ChunkFunction, 9, 16},
		{"node_count", semantic.ChunkFunction, 18, 20},
	})
}

// Defends the extension list: dropping one sends that file to no chunker.
func TestCppChunker_Extensions(t *testing.T) {
	got := map[string]bool{}
	for _, e := range NewCppChunker().SupportedExtensions() {
		got[e] = true
	}
	for _, e := range []string{"cpp", "cxx", "cc", "hpp", "hxx", "hh", "h"} {
		if !got[e] {
			t.Errorf("extension %q not supported", e)
		}
	}
}

// Defends against nameless chunks and forward declarations: a prototype or a
// bodyless class declaration is not code worth a row.
func TestCppChunker_SkipsDeclarationsWithoutBodies(t *testing.T) {
	src := []byte("class Fwd;\nint proto(int);\nnamespace n { int v = 1; }\ntypedef struct { int a; } S;\nnamespace { int anon = 1; }\nenum class E : int;\nenum { A };\nextern \"C\" void f(void);\nint after() { return 1; }\n")
	chunks, err := NewCppChunker().Chunk("a.hpp", src)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	for _, c := range chunks {
		if c.Name == "Fwd" || c.Name == "proto" || c.Name == "E" || c.Name == "f" || c.Name == "" {
			t.Errorf("unexpected chunk %+v", c)
		}
	}
	// A bodyless extern "C" declaration must not stop the walk: the function
	// after it still has to be chunked.
	assertChunks(t, chunks, []wantChunk{{"after", semantic.ChunkFunction, 9, 9}})
}

// Defends preprocessor branches: a function in an #else/#elif arm must be
// indexed, or it vanishes from search with nothing saying so.
func TestCppChunker_PreprocessorBranchesAreWalked(t *testing.T) {
	src := []byte("#ifdef A\nint in_if() { return 1; }\n#elif defined(B)\nint in_elif() { return 2; }\n#else\nint in_else() { return 3; }\n#endif\n")
	chunks, err := NewCppChunker().Chunk("p.h", src)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	assertChunks(t, chunks, []wantChunk{
		{"in_if", semantic.ChunkFunction, 2, 2},
		{"in_elif", semantic.ChunkFunction, 4, 4},
		{"in_else", semantic.ChunkFunction, 6, 6},
	})
}

// Defends operator and template function naming: the name must be the
// callable's own, not the whole declarator.
func TestCppChunker_OperatorAndTemplateFunction(t *testing.T) {
	src := []byte("template<class T> T id(T v) { return v; }\nstruct V { bool operator==(const V&) const { return true; } };\n")
	chunks, err := NewCppChunker().Chunk("a.hpp", src)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	assertChunks(t, chunks, []wantChunk{
		{"id", semantic.ChunkFunction, 1, 1},
		{"operator==", semantic.ChunkMethod, 2, 2},
	})
}
