package treesitter

import (
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// rubySampleChunks is every chunk testdata/ruby/sample.rb must produce, with
// the lines it spans in that file.
var rubySampleChunks = []tsChunk{
	{"Billing", semantic.ChunkFile, 3, 41},
	{"Invoice", semantic.ChunkStruct, 6, 34},
	{"initialize", semantic.ChunkMethod, 9, 11},
	{"total", semantic.ChunkMethod, 13, 15},
	{"paid?", semantic.ChunkMethod, 17, 19},
	{"build", semantic.ChunkMethod, 21, 23},
	{"from_json", semantic.ChunkMethod, 26, 28},
	{"audit", semantic.ChunkMethod, 31, 33},
	{"Formatting", semantic.ChunkFile, 36, 40},
	{"format_amount", semantic.ChunkMethod, 37, 39},
	{"Billing::Refund", semantic.ChunkStruct, 43, 47},
	{"==", semantic.ChunkMethod, 44, 46},
	{"helper", semantic.ChunkFunction, 49, 49},
	{"main", semantic.ChunkFunction, 51, 53},
}

func TestRubyChunker_Extensions(t *testing.T) {
	// The extension list is the registry key: anything but `rb` here either
	// skips Ruby files or hands another language's files to the Ruby grammar.
	got := NewRubyChunker().SupportedExtensions()
	if strings.Join(got, ",") != "rb" {
		t.Errorf("SupportedExtensions() = %v, want [rb]", got)
	}
}

func TestRubyChunker_SampleNamesAndLineRanges(t *testing.T) {
	// T1's done-when: classes, modules and methods come back with the right
	// names and line ranges. The fixture also pins the shapes a naive walk
	// loses: `def self.x`, methods inside `class << self`, `private def x`,
	// a scoped `class A::B`, operator and predicate names, and endless defs.
	// A clean file must report no parse error, or the fallback wrapper
	// windows it as if it were broken.
	chunks, err := NewRubyChunker().Chunk("sample.rb", readTestFile(t, "ruby", "sample.rb"))
	if err != nil {
		t.Fatalf("Chunk returned %v for a clean file", err)
	}
	assertTSChunks(t, chunks, rubySampleChunks)
	for _, c := range chunks {
		if c.Language != "rb" || c.ID == "" || c.Content == "" || c.Signature == "" {
			t.Errorf("chunk %q is not storable: language %q, id %q, %d bytes content, signature %q",
				c.Name, c.Language, c.ID, len(c.Content), c.Signature)
		}
	}
}

func TestRubyChunker_Signature(t *testing.T) {
	// The signature is the definition's first line, which is what a search
	// result shows for the hit.
	chunks, _ := NewRubyChunker().Chunk("sample.rb", readTestFile(t, "ruby", "sample.rb"))
	byName := chunksByName(chunks)
	for name, want := range map[string]string{
		"Invoice": "class Invoice < Base",
		"build":   "def self.build(attrs)",
		"Billing": "module Billing",
	} {
		if got := byName[name].Signature; got != want {
			t.Errorf("%s signature = %q, want %q", name, got, want)
		}
	}
}

func TestRubyChunker_EmptySourceYieldsNoChunks(t *testing.T) {
	// Script-only files are normal Ruby; they must not error or invent chunks.
	for _, src := range []string{"", "require \"json\"\n", "puts 1 + 2\n"} {
		chunks, err := NewRubyChunker().Chunk("e.rb", []byte(src))
		if err != nil || len(chunks) != 0 {
			t.Errorf("Chunk(%q) = %d chunks, err %v; want 0, nil", src, len(chunks), err)
		}
	}
}

func TestRubyChunker_SyntaxErrorIsReported(t *testing.T) {
	// A broken file must say so, so the fallback wrapper can window it and the
	// index output can count it, instead of storing half a parse as complete.
	_, err := NewRubyChunker().Chunk("broken.rb", []byte("class Broken\n  def x(\nend\n"))
	if err == nil {
		t.Error("Chunk returned nil error for a file with a syntax error")
	}
}

func TestRubyChunker_RegisteredForRb(t *testing.T) {
	// The init() registration is what makes `index` pick up .rb files at all.
	f := semantic.NewChunkerFactory()
	RegisterAll(f)
	if _, ok := f.GetChunker("rb"); !ok {
		t.Error("no chunker registered for extension rb")
	}
}

func TestRubyChunker_NamelessNodeIsDropped(t *testing.T) {
	// A node with no name field (here the program root, the shape tree-sitter
	// hands back when it error-recovers a half-written def) must be skipped,
	// not stored as a nameless row that search cannot label.
	c := NewRubyChunker()
	src := []byte("puts 1\n")
	tree, err := ParseSource(src, c.lang)
	if err != nil {
		t.Fatalf("ParseSource: %v", err)
	}
	defer tree.Release()

	var chunks []semantic.Chunk
	c.extractType(tree.RootNode(), src, "a.rb", &chunks, semantic.ChunkStruct)
	if len(chunks) != 0 {
		t.Errorf("emitted %d chunk(s) for a node with no name: %+v", len(chunks), chunks)
	}
}
