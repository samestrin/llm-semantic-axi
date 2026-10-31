package treesitter

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// tsChunk is the part of a chunk the index stores and a caller acts on: the
// name it searches by, the type it filters by, and the lines it opens.
type tsChunk struct {
	Name       string
	Type       semantic.ChunkType
	Start, End int
}

func (c tsChunk) String() string {
	return fmt.Sprintf("%s %v %d-%d", c.Name, c.Type, c.Start, c.End)
}

// tsSampleChunks is every chunk testdata/typescript/sample.ts must produce,
// with the lines it spans in that file.
var tsSampleChunks = []tsChunk{
	{"UserDTO", semantic.ChunkInterface, 3, 7},
	{"UserRole", semantic.ChunkStruct, 9, 9},
	{"UserController", semantic.ChunkStruct, 11, 27},
	{"constructor", semantic.ChunkMethod, 14, 16},
	{"getUser", semantic.ChunkMethod, 18, 21},
	{"deleteUser", semantic.ChunkMethod, 23, 26},
	{"createApp", semantic.ChunkFunction, 29, 32},
	{"handleError", semantic.ChunkFunction, 34, 36},
	{"processData", semantic.ChunkFunction, 38, 40},
	{"Repository", semantic.ChunkInterface, 42, 45},
	{"Result", semantic.ChunkStruct, 47, 47},
	{"Box", semantic.ChunkStruct, 49, 55},
	{"constructor", semantic.ChunkMethod, 50, 50},
	{"map", semantic.ChunkMethod, 52, 54},
	{"identity", semantic.ChunkFunction, 57, 59},
	{"Shape", semantic.ChunkStruct, 61, 67},
	{"describe", semantic.ChunkMethod, 64, 66},
	{"Color", semantic.ChunkStruct, 69, 72},
}

// tsxComponentChunks is the same for testdata/typescript/component.tsx.
var tsxComponentChunks = []tsChunk{
	{"ButtonProps", semantic.ChunkInterface, 3, 6},
	{"Variant", semantic.ChunkStruct, 8, 8},
	{"Button", semantic.ChunkFunction, 10, 16},
	{"Counter", semantic.ChunkFunction, 18, 21},
	{"List", semantic.ChunkFunction, 23, 25},
	{"Panel", semantic.ChunkStruct, 27, 31},
	{"render", semantic.ChunkMethod, 28, 30},
}

func sortedTSChunks(chunks []tsChunk) []string {
	out := make([]string, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, c.String())
	}
	sort.Strings(out)
	return out
}

func asTSChunks(chunks []semantic.Chunk) []tsChunk {
	out := make([]tsChunk, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, tsChunk{c.Name, c.Type, c.StartLine, c.EndLine})
	}
	return out
}

// assertTSChunks compares the whole set, so a missing construct, a wrong
// line range and an extra chunk all fail.
func assertTSChunks(t *testing.T, got []semantic.Chunk, want []tsChunk) {
	t.Helper()
	g, w := sortedTSChunks(asTSChunks(got)), sortedTSChunks(want)
	if strings.Join(g, "\n") != strings.Join(w, "\n") {
		t.Errorf("chunks:\n  got  %v\n  want %v", g, w)
	}
}

func TestTypeScriptChunker_Extensions(t *testing.T) {
	// The extension list is the registry key: `mts` and `cts` missing means
	// those files are skipped by the index, not chunked.
	got := NewTypeScriptChunker().SupportedExtensions()
	want := []string{"ts", "tsx", "mts", "cts"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("SupportedExtensions() = %v, want %v", got, want)
	}
}

func TestJSChunker_NoLongerClaimsTypeScript(t *testing.T) {
	// Both chunkers claiming `ts` panics the registry at startup.
	got := NewJSChunker().SupportedExtensions()
	want := []string{"js", "jsx", "mjs", "cjs"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("SupportedExtensions() = %v, want %v", got, want)
	}
}

func TestTypeScriptChunker_SampleNamesAndLineRanges(t *testing.T) {
	// Interfaces, type aliases, classes (plain, generic, abstract), methods,
	// generic functions, arrow functions and enums, each with the line range
	// a caller opens. A clean file must also report no parse error, or the
	// fallback wrapper windows it as if it were broken.
	chunks, err := NewTypeScriptChunker().Chunk("sample.ts", readTestFile(t, "typescript", "sample.ts"))
	if err != nil {
		t.Fatalf("Chunk returned %v for a clean file", err)
	}
	assertTSChunks(t, chunks, tsSampleChunks)
	for _, c := range chunks {
		if c.Language != "ts" || c.ID == "" || c.Content == "" || c.Signature == "" {
			t.Errorf("chunk %q is not storable: language %q, id %q, %d bytes content, signature %q",
				c.Name, c.Language, c.ID, len(c.Content), c.Signature)
		}
	}
}

func TestTypeScriptChunker_TSXComponentsParseWithTheTSXGrammar(t *testing.T) {
	// The plain TypeScript grammar does not know JSX: before this chunker a
	// .tsx component parsed with errors and its JSX bodies were lost. The tsx
	// grammar must parse it cleanly and find every component.
	chunks, err := NewTypeScriptChunker().Chunk("component.tsx", readTestFile(t, "typescript", "component.tsx"))
	if err != nil {
		t.Fatalf("Chunk returned %v for a clean .tsx file", err)
	}
	assertTSChunks(t, chunks, tsxComponentChunks)
	for _, c := range chunks {
		if c.Language != "tsx" {
			t.Errorf("chunk %q has language %q, want tsx", c.Name, c.Language)
		}
	}
}

func TestTypeScriptChunker_TSXSyntaxInATSFileIsAParseError(t *testing.T) {
	// The grammar is chosen by extension. JSX in a .ts file is invalid
	// TypeScript, and it must come back as a parse error for the fallback to
	// count, not be parsed leniently with the tsx grammar.
	_, err := NewTypeScriptChunker().Chunk("component.ts", readTestFile(t, "typescript", "component.tsx"))
	if !errors.Is(err, ErrParseTree) {
		t.Errorf("Chunk(.ts with JSX) error = %v, want ErrParseTree", err)
	}
}

func TestTypeScriptChunker_ModuleExtensionsUseTheTypeScriptGrammar(t *testing.T) {
	// .mts and .cts are TypeScript with ES-module or CommonJS output; they
	// parse with the same grammar and carry their own language tag.
	src := readTestFile(t, "typescript", "sample.ts")
	for _, ext := range []string{"mts", "cts"} {
		chunks, err := NewTypeScriptChunker().Chunk("sample."+ext, src)
		if err != nil {
			t.Fatalf("Chunk(.%s) returned %v", ext, err)
		}
		assertTSChunks(t, chunks, tsSampleChunks)
		for _, c := range chunks {
			if c.Language != ext {
				t.Errorf(".%s chunk %q has language %q", ext, c.Name, c.Language)
			}
		}
	}
}

func TestTypeScriptChunker_EmptySourceYieldsNoChunks(t *testing.T) {
	// Declaration-only and empty files are normal input for a bulk indexer.
	for _, src := range []string{"", "const PORT = 8080;\n", "import x from 'y';\n"} {
		for _, path := range []string{"a.ts", "a.tsx"} {
			chunks, err := NewTypeScriptChunker().Chunk(path, []byte(src))
			if err != nil {
				t.Errorf("Chunk(%s, %q) returned error: %v", path, src, err)
			}
			if len(chunks) != 0 {
				t.Errorf("Chunk(%s, %q) = %v, want none", path, src, chunks)
			}
		}
	}
}
