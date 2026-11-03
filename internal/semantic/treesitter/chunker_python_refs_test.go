package treesitter

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// pythonRefsFixture covers each shape the task names: direct calls, self. and
// cls. method calls, imports, and a decorated function, plus a class whose
// methods make calls the class itself does not.
const pythonRefsFixture = `import os.path
from .helpers import load, save as store

def top():
    helper()
    load()

@app.route(register("/x"))
def decorated():
    helper()

class Repo:
    def get(self):
        self.fetch()
        return helper()

    @classmethod
    def make(cls):
        return cls.build()

def helper():
    helper()
`

// fakeEmbedder returns a fixed vector, so indexing needs no endpoint.
type fakeEmbedder struct{}

func (fakeEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3, 0.4}, nil
}
func (fakeEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{0.1, 0.2, 0.3, 0.4}
	}
	return out, nil
}
func (fakeEmbedder) Dimensions() int { return 4 }
func (fakeEmbedder) Model() string   { return "fake" }

// newPythonIndex returns a real sqlite index fed by the factory the CLI
// builds, so the chunker under test is the Fallback-wrapped one the index
// actually sees rather than a bare PythonChunker.
func newPythonIndex(t *testing.T) (*semantic.SQLiteStorage, *semantic.IndexManager, string) {
	t.Helper()
	storage, err := semantic.NewSQLiteStorage(filepath.Join(t.TempDir(), "semantic.db"), 4)
	if err != nil {
		t.Fatalf("NewSQLiteStorage: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	factory := semantic.NewChunkerFactory()
	RegisterAll(factory)
	if c, ok := factory.GetChunker("py"); !ok {
		t.Fatal("no chunker registered for .py")
	} else if _, wrapped := c.(*Fallback); !wrapped {
		t.Fatalf("registered .py chunker is %T, want the *Fallback the index uses", c)
	}
	return storage, semantic.NewIndexManager(storage, fakeEmbedder{}, factory), t.TempDir()
}

func writePython(t *testing.T, dir, name, src string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// storedRefs returns each indexed chunk's stored edges as "type:name",
// sorted, keyed by chunk name.
func storedRefs(t *testing.T, storage *semantic.SQLiteStorage) map[string][]string {
	t.Helper()
	ctx := context.Background()
	chunks, err := storage.List(ctx, semantic.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := make(map[string][]string, len(chunks))
	for _, c := range chunks {
		refs, err := storage.GetRefs(ctx, c.ID)
		if err != nil {
			t.Fatalf("GetRefs(%s): %v", c.Name, err)
		}
		edges := []string{}
		for _, r := range refs {
			edges = append(edges, string(r.RefType)+":"+r.RefName)
		}
		sort.Strings(edges)
		got[c.Name] = edges
	}
	return got
}

// Defends the epic 03 T1 success line: Python files indexed the way the CLI
// indexes them get call and import edges. Before T1 the Fallback wrapper hid
// any RefExtractor, so even a PythonChunker that extracted refs would have
// stored none. It also pins two ways copying GoChunker would go wrong for
// Python: an attribute access is not a type use, so no uses_type row; and a
// class chunk spans its methods, so attributing by line range alone would
// make Repo a caller of everything get and make call.
func TestPythonRefs_IndexedThroughTheRegisteredChunker(t *testing.T) {
	storage, mgr, dir := newPythonIndex(t)
	writePython(t, dir, "repo.py", pythonRefsFixture)
	if _, err := mgr.Index(context.Background(), dir, semantic.IndexOptions{}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	got := storedRefs(t, storage)
	want := map[string][]string{
		// Top-level imports go to the file's first chunk, as GoChunker does.
		"top":       {"calls:helper", "calls:load", "imports:load", "imports:path", "imports:save"},
		"decorated": {"calls:helper", "calls:register"},
		"Repo":      {},
		"get":       {"calls:helper", "calls:self.fetch"},
		"make":      {"calls:cls.build"},
		// A call to itself is not an edge, as in GoChunker.
		"helper": {},
	}
	for name, edges := range want {
		if strings.Join(got[name], ",") != strings.Join(edges, ",") {
			t.Errorf("%s edges = %v, want %v", name, got[name], edges)
		}
	}
	for name, edges := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected chunk %q with edges %v", name, edges)
		}
		for _, e := range edges {
			if strings.HasPrefix(e, string(semantic.RefUsesType)+":") {
				t.Errorf("%s has a uses_type edge %q; a Python attribute access is not a type use", name, e)
			}
		}
	}
}

// Defends against stale Python edges: an edit that removes a function's last
// call must leave it with none, through both the full index and the
// incremental update, and the edge must not survive under the chunk's
// unchanged ID.
func TestPythonRefs_EditDroppingLastCallClearsItsEdges(t *testing.T) {
	for name, reindex := range map[string]func(context.Context, *semantic.IndexManager, string) error{
		// A plain index of an edited file fails on a duplicate chunk ID
		// (filed as debt), so the full path is a --force re-index.
		"index --force": func(ctx context.Context, mgr *semantic.IndexManager, dir string) error {
			_, err := mgr.Index(ctx, dir, semantic.IndexOptions{Force: true})
			return err
		},
		"update": func(ctx context.Context, mgr *semantic.IndexManager, dir string) error {
			_, err := mgr.Update(ctx, dir, semantic.UpdateOptions{})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			storage, mgr, dir := newPythonIndex(t)
			ctx := context.Background()

			writePython(t, dir, "a.py", "def a():\n    b()\n\ndef b():\n    pass\n")
			if _, err := mgr.Index(ctx, dir, semantic.IndexOptions{}); err != nil {
				t.Fatalf("Index: %v", err)
			}
			if got := storedRefs(t, storage)["a"]; len(got) != 1 || got[0] != "calls:b" {
				t.Fatalf("precondition: a edges = %v, want [calls:b]", got)
			}

			writePython(t, dir, "a.py", "def a():\n    pass\n\ndef b():\n    pass\n")
			if err := reindex(ctx, mgr, dir); err != nil {
				t.Fatalf("re-index: %v", err)
			}
			for chunk, edges := range storedRefs(t, storage) {
				if len(edges) != 0 {
					t.Errorf("%s still has edges %v after the edit removed the file's last call", chunk, edges)
				}
			}
		})
	}
}

// Defends "a wrong edge is worse than none" for code that does not parse:
// the fallback puts a text window over the lines around the error, and a call
// the grammar still found there (bad) must not become an edge from that
// window, while the clean function keeps its own.
func TestPythonRefs_NoEdgeFromAFallbackTextWindow(t *testing.T) {
	src := []byte("def ok():\n    good()\n\nbad(1)\nx = = 2\n")
	f := WithFallback(NewPythonChunker())
	chunks, err := f.Chunk("b.py", src)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	refs, err := f.ExtractRefs("b.py", src, chunks)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	byID := map[string]semantic.Chunk{}
	windows := 0
	for _, c := range chunks {
		byID[c.ID] = c
		if c.Type == semantic.ChunkFile {
			windows++
		}
	}
	if windows == 0 {
		t.Fatalf("precondition: no text window in %+v; the fixture no longer falls back", chunks)
	}
	if len(refs) != 1 || refs[0].RefName != "good" {
		t.Errorf("refs = %+v, want only ok's call to good", refs)
	}
	for _, r := range refs {
		c := byID[r.ChunkID]
		if c.Type == semantic.ChunkFile {
			t.Errorf("edge %s:%s attributed to a text window %+v", r.RefType, r.RefName, c)
		}
	}
}

// Defends where import edges land and what they name: an import inside a
// function belongs to that function, not to the file's first chunk, and a
// wildcard import names no symbol, so it records no edge.
func TestPythonRefs_LocalAndWildcardImports(t *testing.T) {
	src := []byte("from m import *\n\ndef first():\n    pass\n\ndef f():\n    import json\n")
	c := NewPythonChunker()
	chunks, err := c.Chunk("i.py", src)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	refs, err := c.ExtractRefs("i.py", src, chunks)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	names := map[string]string{}
	for _, ch := range chunks {
		names[ch.ID] = ch.Name
	}
	var got []string
	for _, r := range refs {
		got = append(got, names[r.ChunkID]+" "+string(r.RefType)+":"+r.RefName)
	}
	if strings.Join(got, ",") != "f imports:json" {
		t.Errorf("refs = %v, want [f imports:json]", got)
	}
}

// Defends "a wrong edge is worse than none": a call whose target the index
// cannot resolve writes no edge. obj.get() must not become calls:get, which
// would link it to the lone chunk named get; a dynamic call (call result,
// subscript, chained attribute) names nothing at all. Only the bare name and
// the self. receiver, which do resolve, are recorded.
func TestPythonRefs_UnresolvableCallsWriteNoEdge(t *testing.T) {
	storage, mgr, dir := newPythonIndex(t)
	writePython(t, dir, "m.py", `def get():
    pass

def user(obj, table, handlers):
    obj.get()
    obj.a.get()
    self.cache.get()
    table["get"]()
    handlers[0]()
    factory()()
    getattr(obj, "get")()
    (lambda: 1)()

class Box:
    def run(self):
        self.get()
`)
	if _, err := mgr.Index(context.Background(), dir, semantic.IndexOptions{}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	got := storedRefs(t, storage)
	if _, ok := got["get"]; !ok {
		t.Fatal("precondition: the lone get chunk must be indexed")
	}
	// Only the calls that name a real target: the bare names factory and
	// getattr (a genuine call to the builtin), never get.
	if want := "calls:factory,calls:getattr"; strings.Join(got["user"], ",") != want {
		t.Errorf("user edges = %v, want %s", got["user"], want)
	}
	// self.get() is resolvable, so it is still recorded.
	if want := "calls:self.get"; strings.Join(got["run"], ",") != want {
		t.Errorf("run edges = %v, want %s", got["run"], want)
	}
}

// pythonRefStrings extracts refs and renders each as "chunk type:name".
func pythonRefStrings(t *testing.T, src string) []string {
	t.Helper()
	c := NewPythonChunker()
	chunks, err := c.Chunk("m.py", []byte(src))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	refs, err := c.ExtractRefs("m.py", []byte(src), chunks)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	names := map[string]string{}
	for _, ch := range chunks {
		names[ch.ID] = ch.Name
	}
	var got []string
	for _, r := range refs {
		got = append(got, names[r.ChunkID]+" "+string(r.RefType)+":"+r.RefName)
	}
	return got
}

// Defends callers on a script entry point: main() under the __main__ guard lies
// in no chunk, and used to write no edge, so `callers main` answered empty for
// a function whose only caller is the script body. It belongs to the first
// chunk, as imports do.
func TestPythonRefs_ModuleLevelCallAttributedToFirstChunk(t *testing.T) {
	got := pythonRefStrings(t, "def main():\n    pass\n\nif __name__ == \"__main__\":\n    main()\n")
	if strings.Join(got, ",") != "main calls:main" {
		t.Errorf("refs = %v, want [main calls:main]", got)
	}
}

// Defends the recursion filter not eating module-level calls: a module-level
// call to the first chunk's own name is not recursion, so its edge is kept.
func TestPythonRefs_ModuleLevelCallToFirstChunkNameKeepsEdge(t *testing.T) {
	got := pythonRefStrings(t, "def run():\n    pass\n\nrun()\n")
	if strings.Join(got, ",") != "run calls:run" {
		t.Errorf("refs = %v, want [run calls:run]", got)
	}
}

// Defends "nothing to attribute to": a file with a call and no chunk yields no
// edge and no error rather than a panic or an edge with an empty chunk ID.
func TestPythonRefs_CallWithNoChunkYieldsNoEdge(t *testing.T) {
	c := NewPythonChunker()
	refs, err := c.ExtractRefs("s.py", []byte("print(1)\n"), nil)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("refs = %v, want none", refs)
	}
}
