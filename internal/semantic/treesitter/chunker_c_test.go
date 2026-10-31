package treesitter

import (
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// Line numbers below are pinned by the tests, so edit the sample with care.
const cSample = `#include <stdio.h>

struct Point {
    int x;
    int y;
};

enum Color { RED, GREEN };

union Value { int i; float f; };

typedef struct {
    int id;
} Record;

typedef int (*callback_t)(int);

struct Fwd;

int declared_only(int a);

static char *dup_string(const char *s) {
    return 0;
}

int main(void) {
    return 0;
}

#ifdef FEATURE
void guarded(void) {}
#endif
`

func TestCChunker_Extensions(t *testing.T) {
	// ".h" must stay out: the C++ chunker claims it, and the registry panics on a double claim.
	got := NewCChunker().SupportedExtensions()
	if strings.Join(got, ",") != "c" {
		t.Errorf("SupportedExtensions() = %v, want [c]", got)
	}
}

func TestCChunker_NamesTypesAndLineRanges(t *testing.T) {
	// Defends the epic's done-when: every definition comes back with the right
	// name and line range, including pointer-returning functions, anonymous
	// typedef'd structs, function-pointer typedefs and #ifdef-guarded code.
	chunks, err := NewCChunker().Chunk("sample.c", []byte(cSample))
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	byName := chunksByName(chunks)

	want := map[string]struct {
		typ        semantic.ChunkType
		start, end int
	}{
		"Point":      {semantic.ChunkStruct, 3, 6},
		"Color":      {semantic.ChunkStruct, 8, 8},
		"Value":      {semantic.ChunkStruct, 10, 10},
		"Record":     {semantic.ChunkStruct, 12, 14},
		"callback_t": {semantic.ChunkStruct, 16, 16},
		"dup_string": {semantic.ChunkFunction, 22, 24},
		"main":       {semantic.ChunkFunction, 26, 28},
		"guarded":    {semantic.ChunkFunction, 31, 31},
	}
	for name, w := range want {
		c, ok := byName[name]
		if !ok {
			t.Errorf("missing chunk %q in %v", name, chunks)
			continue
		}
		if c.Type != w.typ || c.StartLine != w.start || c.EndLine != w.end {
			t.Errorf("%q = %v %d-%d, want %v %d-%d", name, c.Type, c.StartLine, c.EndLine, w.typ, w.start, w.end)
		}
		if c.Language != "c" || c.ID == "" || c.Content == "" {
			t.Errorf("%q has unstorable fields: %+v", name, c)
		}
	}
	// Forward declarations and prototypes are not definitions; indexing them
	// would show a hit that has no body.
	for _, name := range []string{"Fwd", "declared_only"} {
		if _, ok := byName[name]; ok {
			t.Errorf("%q is a declaration, not a definition, and must not be chunked", name)
		}
	}
	if len(chunks) != len(want) {
		t.Errorf("got %d chunks, want %d: %v", len(chunks), len(want), chunks)
	}
}

func TestCChunker_Signature(t *testing.T) {
	// The signature is the first line without the opening brace.
	chunks, _ := NewCChunker().Chunk("sample.c", []byte(cSample))
	if got := chunksByName(chunks)["dup_string"].Signature; got != "static char *dup_string(const char *s) {"[:len("static char *dup_string(const char *s)")] {
		t.Errorf("signature = %q", got)
	}
}

func TestCChunker_EmptySourceYieldsNoChunks(t *testing.T) {
	// Include-only or empty files are normal input; they must not error.
	for _, src := range []string{"", "#include <x.h>\n", "int g;\n"} {
		chunks, err := NewCChunker().Chunk("e.c", []byte(src))
		if err != nil || len(chunks) != 0 {
			t.Errorf("Chunk(%q) = %d chunks, err %v; want 0, nil", src, len(chunks), err)
		}
	}
}

func TestCChunker_RegisteredForC(t *testing.T) {
	// The init() registration is what makes `index` pick up .c files at all.
	f := semantic.NewChunkerFactory()
	RegisterAll(f)
	if _, ok := f.GetChunker("c"); !ok {
		t.Error("no chunker registered for extension c")
	}
}
