package treesitter

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

func csharpFixture(t *testing.T) []semantic.Chunk {
	t.Helper()
	chunks, err := NewCSharpChunker().Chunk("sample.cs", readTestFile(t, "csharp", "sample.cs"))
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	return chunks
}

func TestCSharpChunker_Extensions(t *testing.T) {
	// The extension is the dispatcher's registry key; "cs" only. Without it
	// every .cs file is skipped and never indexed.
	got := NewCSharpChunker().SupportedExtensions()
	if len(got) != 1 || got[0] != "cs" {
		t.Errorf("SupportedExtensions() = %v, want [cs]", got)
	}
}

func TestCSharpChunker_NamesTypesAndLineRanges(t *testing.T) {
	// Defends the T3 "done when": namespaces (block and file-scoped), classes,
	// interfaces, structs, records and methods come back with the right name,
	// type and 1-based line range. An off-by-one here makes every search hit
	// open the wrong lines; a missing kind is code the index cannot find.
	chunks := csharpFixture(t)
	byKey := map[string]semantic.Chunk{}
	for _, c := range chunks {
		byKey[c.Name+"/"+c.Type.String()+"/"+strconv.Itoa(c.StartLine)] = c
	}

	// OrderService starts at its [Serializable] attribute (line 10), which is
	// part of the declaration. The file-scoped namespace runs from its own
	// line to the end of the file, as a block namespace runs to its brace.
	want := []struct {
		name       string
		typ        semantic.ChunkType
		start, end int
	}{
		{"Shop.Orders", semantic.ChunkFile, 3, 51},
		{"IRepository", semantic.ChunkInterface, 5, 8},
		{"Find", semantic.ChunkMethod, 7, 7},
		{"OrderService", semantic.ChunkStruct, 10, 36},
		{"OrderService", semantic.ChunkMethod, 15, 18},
		{"OrderService", semantic.ChunkMethod, 20, 20},
		{"Find", semantic.ChunkMethod, 22, 25},
		{"Cache", semantic.ChunkStruct, 29, 35},
		{"Clear", semantic.ChunkMethod, 31, 34},
		{"Money", semantic.ChunkStruct, 38, 43},
		{"Add", semantic.ChunkMethod, 42, 42},
		{"Order", semantic.ChunkStruct, 45, 48},
		{"IsFree", semantic.ChunkMethod, 47, 47},
		{"Status", semantic.ChunkStruct, 50, 50},
		{"Shop.Billing", semantic.ChunkFile, 53, 63},
		{"Invoice", semantic.ChunkStruct, 55, 55},
		{"Extensions", semantic.ChunkStruct, 57, 63},
		{"Shout", semantic.ChunkMethod, 59, 62},
	}
	for _, w := range want {
		c, ok := byKey[w.name+"/"+w.typ.String()+"/"+strconv.Itoa(w.start)]
		if !ok {
			t.Errorf("missing %s chunk %q at line %d in %v", w.typ, w.name, w.start, chunks)
			continue
		}
		if c.EndLine != w.end {
			t.Errorf("%q (%s) lines = %d-%d, want %d-%d", w.name, w.typ, c.StartLine, c.EndLine, w.start, w.end)
		}
	}
	if len(chunks) != len(want) {
		t.Errorf("got %d chunks, want %d: %v", len(chunks), len(want), chunks)
	}
}

func TestCSharpChunker_SignatureDropsAttributes(t *testing.T) {
	// [Serializable] opens the class's text. ExtractSignature alone would make
	// it the signature, which is what results display, and says nothing about
	// the class. The attribute must stay in the content.
	var svc semantic.Chunk
	for _, c := range csharpFixture(t) {
		if c.Name == "OrderService" && c.Type == semantic.ChunkStruct {
			svc = c
		}
	}
	if svc.Signature != "public class OrderService : IRepository" {
		t.Errorf("signature = %q", svc.Signature)
	}
	if !strings.HasPrefix(svc.Content, "[Serializable]") {
		t.Errorf("content lost its attribute: %q", svc.Content)
	}
}

func TestCSharpChunker_ChunkFields(t *testing.T) {
	// Every chunk must be storable: ID, language tag, path, valid line range.
	for _, c := range csharpFixture(t) {
		if c.ID == "" || c.Language != "cs" || c.FilePath != "sample.cs" ||
			c.StartLine <= 0 || c.EndLine < c.StartLine || c.Content == "" || c.Signature == "" {
			t.Errorf("bad chunk: %+v", c)
		}
	}
}

func TestCSharpChunker_EmptySourceYieldsNoChunks(t *testing.T) {
	// Empty or declaration-free files are normal input: no error, no chunks.
	for _, src := range []string{"", "using System;\n"} {
		chunks, err := NewCSharpChunker().Chunk("E.cs", []byte(src))
		if err != nil || len(chunks) != 0 {
			t.Errorf("Chunk(%q) = %d chunks, err %v; want 0, nil", src, len(chunks), err)
		}
	}
}

func TestCSharpChunker_SyntaxErrorIsReported(t *testing.T) {
	// A broken file must return ErrParseTree so the fallback wrapper can window
	// it instead of the walk silently passing a partial file off as whole.
	_, err := NewCSharpChunker().Chunk("B.cs", []byte("class A { void f( { \n"))
	if !errors.Is(err, ErrParseTree) {
		t.Errorf("err = %v, want ErrParseTree", err)
	}
}

func TestCSharpChunker_TreeThatDroppedDeclarationsIsAParseError(t *testing.T) {
	// gotreesitter v0.13.4 parses this unclosed class into a tree with no
	// ERROR node that has lost `A` and `N` entirely, and leaks an internal
	// `_class_declaration_initializer` node in their place. TreeError trusts
	// HasError, so without this guard the file indexes as a clean parse with
	// two declarations silently missing.
	src := "class A { }\nnamespace N { }\nclass E { void F(int x) { }\n"
	_, err := NewCSharpChunker().Chunk("D.cs", []byte(src))
	if !errors.Is(err, ErrParseTree) {
		t.Errorf("err = %v, want ErrParseTree", err)
	}
}

func TestCSharpChunker_NamelessDeclarationIsDropped(t *testing.T) {
	// The name guard: a node without a name field must not become a chunk.
	c := NewCSharpChunker()
	src := []byte("using System;\n")
	tree, err := ParseSource(src, c.lang)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Release()
	var out []semantic.Chunk
	c.extract(tree.RootNode(), src, "a.cs", &out, semantic.ChunkStruct)
	if len(out) != 0 {
		t.Errorf("emitted chunks for nameless nodes: %+v", out)
	}
}
