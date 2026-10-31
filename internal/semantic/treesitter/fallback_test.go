package treesitter

import (
	"errors"
	"strings"
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

// partlyBrokenPython parses up to `ok`; the error swallows `after`, so the
// walk returns `ok` alone and lines 6-9 would drop without the gap windows.
const partlyBrokenPython = "import os\n\ndef ok(x):\n    return x\n\nwhile True print(\n\ndef after(y):\n    return y\n"

// Defends AC2 at its source: a tree with an error node used to yield whatever
// nodes the walk happened to find, so a broken file indexed as a partial (or
// empty) set of chunks and nothing said so.
func TestTreeError_ReportsATreeWithAnErrorNode(t *testing.T) {
	lang := NewPythonChunker().lang
	tree, err := ParseSource([]byte(brokenPython), lang)
	if err != nil {
		t.Fatalf("ParseSource(broken): %v", err)
	}
	if err := TreeError(tree); !errors.Is(err, ErrParseTree) {
		t.Errorf("TreeError(broken) = %v, want ErrParseTree", err)
	}
	tree.Release()

	tree, err = ParseSource([]byte(cleanPython), lang)
	if err != nil {
		t.Fatalf("ParseSource(clean): %v", err)
	}
	if err := TreeError(tree); err != nil {
		t.Errorf("TreeError(clean) = %v, want nil", err)
	}
	tree.Release()
}

// brokenSamples holds, per extension, source whose first definition parses
// and whose tail does not.
var brokenSamples = map[string]string{
	"py": "def ok(x):\n    return x\n\nwhile True print(\n", "pyw": "def ok(x):\n    return x\n\nwhile True print(\n", "pyi": "def ok(x):\n    return x\n\nwhile True print(\n",
	"js": "function ok() { return 1 }\nfunction (\n", "jsx": "function ok() { return 1 }\nfunction (\n", "mjs": "function ok() { return 1 }\nfunction (\n", "cjs": "function ok() { return 1 }\nfunction (\n",
	"ts": "function ok() { return 1 }\nfunction (\n", "tsx": "function ok() { return 1 }\nfunction (\n",
	"php": "<?php\nfunction ok() { return 1; }\nfunction broken( {\n", "phtml": "<?php\nfunction ok() { return 1; }\nfunction broken( {\n", "php5": "<?php\nfunction ok() { return 1; }\nfunction broken( {\n", "php7": "<?php\nfunction ok() { return 1; }\nfunction broken( {\n",
	"rs": "fn ok() {}\nfn broken( {\n",
}

// Defends the contract every chunker owes the wrapper: return the chunks the
// walk found together with ErrParseTree. A chunker that returns nil error on a
// broken tree is never counted; one that returns no chunks loses the named
// definitions the board chose to keep (EST-3580, hybrid). A new chunker that
// registers an extension without a sample here fails this test on purpose.
func TestChunkers_ReturnTheirChunksWithErrParseTree(t *testing.T) {
	factory := semantic.NewChunkerFactory()
	RegisterAll(factory)
	for _, ext := range factory.SupportedExtensions() {
		src, ok := brokenSamples[ext]
		if !ok {
			t.Errorf("no broken sample for %q: add one so its chunker is proven to report parse errors", ext)
			continue
		}
		c, _ := factory.GetChunker(ext)
		chunks, err := unwrap(c).Chunk("a."+ext, []byte(src))
		if !errors.Is(err, ErrParseTree) {
			t.Errorf("%s: err = %v, want ErrParseTree", ext, err)
		}
		if len(chunks) == 0 || chunks[0].Name != "ok" {
			t.Errorf("%s: chunks = %+v, want the parsed `ok` kept", ext, chunks)
		}
	}
}

// Defends the blank-gap skip: the blank lines between two parsed functions
// are not dropped code, and a whitespace-only window would be an empty search
// hit embedded for nothing.
func TestFallback_BlankGapsBetweenNamedChunksGetNoWindow(t *testing.T) {
	src := "def a():\n    return 1\n    \n\t\ndef b():\n    return 2\n\nwhile True print(\n"
	chunks, fellBack, err := WithFallback(NewPythonChunker()).ChunkWithFallback("g.py", []byte(src))
	if err != nil || !fellBack {
		t.Fatalf("ChunkWithFallback: fellBack=%v err=%v", fellBack, err)
	}
	for _, c := range chunks {
		if c.Type == semantic.ChunkFile && strings.TrimSpace(c.Content) == "" {
			t.Errorf("window %d-%d holds only whitespace", c.StartLine, c.EndLine)
		}
	}
	if len(chunks) != 3 {
		t.Errorf("got %d chunks, want a, b and one window for the broken tail: %+v", len(chunks), chunks)
	}
}

// Defends the board's choice (EST-3580, hybrid): a partly parsed file keeps
// its named chunks, and only the lines no chunk covers become text windows.
// Replacing the whole file threw away 610 named chunks from 36 valid CPython
// stdlib files that gotreesitter flags; keeping only the named chunks would
// drop `after` here without a word.
func TestFallback_PartlyParsedFileKeepsNamedChunksAndWindowsTheRest(t *testing.T) {
	chunks, fellBack, err := WithFallback(NewPythonChunker()).ChunkWithFallback("p.py", []byte(partlyBrokenPython))
	if err != nil {
		t.Fatalf("ChunkWithFallback: %v", err)
	}
	if !fellBack {
		t.Error("a partly parsed file was not reported as a fallback")
	}

	var named, windows []semantic.Chunk
	for _, c := range chunks {
		if c.Type == semantic.ChunkFile {
			windows = append(windows, c)
		} else {
			named = append(named, c)
		}
	}
	if len(named) != 1 || named[0].Name != "ok" || named[0].StartLine != 3 || named[0].EndLine != 4 {
		t.Fatalf("named chunks = %+v, want `ok` at lines 3-4", named)
	}

	var text strings.Builder
	for _, w := range windows {
		if w.StartLine <= 4 && w.EndLine >= 3 {
			t.Errorf("window %d-%d overlaps the named chunk at 3-4", w.StartLine, w.EndLine)
		}
		if strings.TrimSpace(w.Content) == "" {
			t.Errorf("window %d-%d holds only whitespace", w.StartLine, w.EndLine)
		}
		text.WriteString(w.Content)
	}
	for _, want := range []string{"import os", "while True print(", "def after(y):"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("no text window holds %q; that line dropped", want)
		}
	}
	for _, w := range windows {
		if strings.Contains(w.Content, "def after(y):") && (w.StartLine > 8 || w.EndLine < 8) {
			t.Errorf("window holding `def after` spans %d-%d, which does not include its line 8", w.StartLine, w.EndLine)
		}
	}
	for i := 1; i < len(chunks); i++ {
		if chunks[i].StartLine < chunks[i-1].StartLine {
			t.Errorf("chunks are not in line order: %d after %d", chunks[i].StartLine, chunks[i-1].StartLine)
		}
	}
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

// Defends the line between the two fallbacks: only ErrParseTree vouches for
// the chunks returned with it. A chunker that fails any other way may hand
// back half-built chunks, and keeping them would mix them with windows over
// the rest of the file. The whole file must be windowed instead.
func TestFallback_NonParseErrorDiscardsPartialChunks(t *testing.T) {
	half := semantic.Chunk{FilePath: "a.stub", Type: semantic.ChunkFunction, Name: "half", StartLine: 1, EndLine: 1}
	inner := &stubChunker{chunks: []semantic.Chunk{half}, err: errors.New("parser gave up")}

	chunks, fellBack, err := WithFallback(inner).ChunkWithFallback("a.stub", []byte("line one\nline two\nline three"))
	if err != nil {
		t.Fatalf("ChunkWithFallback: %v", err)
	}
	if !fellBack {
		t.Error("the failure was not reported as a fallback")
	}
	assertTextWindows(t, chunks, "a.stub") // fails on the kept `half` chunk
	if last := chunks[len(chunks)-1]; last.EndLine != 3 {
		t.Errorf("windows end at line %d, want 3: the whole file", last.EndLine)
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
