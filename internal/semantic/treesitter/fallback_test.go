package treesitter

import (
	"errors"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// brokenPython does not parse: the def has no colon and the call is unclosed.
const brokenPython = "def broken(x\n    return x +\n\nclass Fine:\n    pass\n"

const cleanPython = "def ok(x):\n    return x\n"

// stubChunker returns whatever it was told to, so a test can drive the
// wrapper down each path without needing source a grammar rejects.
type stubChunker struct {
	chunks []semantic.Chunk
	err    error
	panics bool
}

func (s *stubChunker) Chunk(string, []byte) ([]semantic.Chunk, error) {
	if s.panics {
		panic("grammar blew up")
	}
	return s.chunks, s.err
}
func (s *stubChunker) SupportedExtensions() []string { return []string{"stub"} }

func assertTextWindows(t *testing.T, chunks []semantic.Chunk, path string) {
	t.Helper()
	if len(chunks) == 0 {
		t.Fatal("fallback produced no chunks; the file would drop silently")
	}
	for _, c := range chunks {
		if c.Type != semantic.ChunkFile {
			t.Errorf("chunk %q has type %q, want the generic chunker's %q", c.Name, c.Type, semantic.ChunkFile)
		}
		if c.FilePath != path {
			t.Errorf("chunk path = %q, want %q", c.FilePath, path)
		}
	}
	if chunks[0].StartLine != 1 {
		t.Errorf("first text window starts at line %d, want 1", chunks[0].StartLine)
	}
}

// Defends AC2 at its source: a tree with an error node used to yield whatever
// nodes the walk happened to find, so a broken file indexed as a partial (or
// empty) set of chunks and nothing said so.
func TestParseSource_ReportsATreeWithAnErrorNode(t *testing.T) {
	_, err := ParseSource([]byte(brokenPython), NewPythonChunker().lang)
	if !errors.Is(err, ErrParseTree) {
		t.Fatalf("ParseSource(broken) err = %v, want ErrParseTree", err)
	}

	tree, err := ParseSource([]byte(cleanPython), NewPythonChunker().lang)
	if err != nil {
		t.Fatalf("ParseSource(clean) err = %v", err)
	}
	tree.Release()
}

// Defends the fallback itself: an unparseable file must come back as text
// windows from the generic chunker and be flagged, never as nothing.
func TestFallback_UnparseableFileBecomesTextWindows(t *testing.T) {
	w := WithFallback(NewPythonChunker())

	chunks, fellBack, err := w.ChunkWithFallback("pkg/broken.py", []byte(brokenPython))
	if err != nil {
		t.Fatalf("ChunkWithFallback: %v", err)
	}
	if !fellBack {
		t.Error("an unparseable file was not reported as a fallback")
	}
	assertTextWindows(t, chunks, "pkg/broken.py")
}

// Defends the clean path: a file that parses keeps its functions and classes
// and is not counted, or the count would mean nothing.
func TestFallback_CleanFileKeepsTreeSitterChunks(t *testing.T) {
	w := WithFallback(NewPythonChunker())

	chunks, fellBack, err := w.ChunkWithFallback("ok.py", []byte(cleanPython))
	if err != nil {
		t.Fatalf("ChunkWithFallback: %v", err)
	}
	if fellBack {
		t.Error("a clean file was reported as a fallback")
	}
	if len(chunks) != 1 || chunks[0].Name != "ok" || chunks[0].Type != semantic.ChunkFunction {
		t.Errorf("chunks = %+v, want the one function `ok`", chunks)
	}
}

// Defends the plain Chunker path: callers that do not ask about fallback
// (anything holding a semantic.Chunker) still get text windows, not an error.
func TestFallback_ChunkAlsoFallsBack(t *testing.T) {
	w := WithFallback(NewPythonChunker())

	chunks, err := w.Chunk("broken.py", []byte(brokenPython))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	assertTextWindows(t, chunks, "broken.py")
}

// Defends "a file never drops silently" against the two other ways a chunker
// can fail: returning an error, and panicking on input its grammar mishandles.
// Before the wrapper, the first skipped the file and the second killed the run.
func TestFallback_ChunkerErrorAndPanicFallBack(t *testing.T) {
	cases := map[string]*stubChunker{
		"error": {err: errors.New("parser gave up")},
		"panic": {panics: true},
	}
	for name, inner := range cases {
		t.Run(name, func(t *testing.T) {
			chunks, fellBack, err := WithFallback(inner).ChunkWithFallback("a.stub", []byte("line one\nline two\n"))
			if err != nil {
				t.Fatalf("ChunkWithFallback: %v", err)
			}
			if !fellBack {
				t.Error("the failure was not reported as a fallback")
			}
			assertTextWindows(t, chunks, "a.stub")
		})
	}
}

// Defends the wrapper's transparency: it must claim exactly the inner
// chunker's extensions, or the registry would route files to the wrong place.
func TestFallback_ForwardsExtensionsAndUnwraps(t *testing.T) {
	inner := NewRustChunker()
	w := WithFallback(inner)

	got, want := w.SupportedExtensions(), inner.SupportedExtensions()
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("SupportedExtensions = %v, want %v", got, want)
	}
	if w.Unwrap() != inner {
		t.Errorf("Unwrap = %T, want the inner chunker", w.Unwrap())
	}
}

// Defends "a wrapper the registry applies to every tree-sitter chunker": a
// chunker registered without it would drop broken files with no count, and
// T3-T6 add chunkers that must get the fallback without opting in.
func TestRegistry_WrapsEveryChunkerInTheFallback(t *testing.T) {
	r := NewRegistry()
	r.Add(func() semantic.Chunker { return &fakeChunker{exts: []string{"fake"}} })
	factory := semantic.NewChunkerFactory()
	r.RegisterInto(factory)

	c, ok := factory.GetChunker("fake")
	if !ok {
		t.Fatal("fake chunker not registered")
	}
	if _, ok := c.(semantic.FallbackChunker); !ok {
		t.Errorf("registered chunker is %T, not wrapped in the fallback", c)
	}

	factory = semantic.NewChunkerFactory()
	RegisterAll(factory)
	for _, ext := range factory.SupportedExtensions() {
		c, _ := factory.GetChunker(ext)
		if _, ok := c.(semantic.FallbackChunker); !ok {
			t.Errorf("extension %q registered to %T without the fallback", ext, c)
		}
	}
}
