package semantic

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// symbolLanguagesChunks gives "Shared" a chunk in Go, which has an edge, and in
// Python, which has none, so one lookup must report both answers.
var symbolLanguagesChunks = []Chunk{
	{ID: "go-shared", FilePath: "a.go", Type: ChunkFunction, Name: "Shared", Content: "x", StartLine: 1, EndLine: 2, Language: "go"},
	{ID: "go-callee", FilePath: "b.go", Type: ChunkFunction, Name: "Callee", Content: "x", StartLine: 1, EndLine: 2, Language: "go"},
	{ID: "py-shared", FilePath: "c.py", Type: ChunkFunction, Name: "Shared", Content: "x", StartLine: 1, EndLine: 2, Language: "py"},
}

var symbolLanguagesRefs = []ChunkRef{{ChunkID: "go-shared", RefType: RefCalls, RefName: "Callee"}}

// assertSymbolLanguages defends the lookup `callers` and `refs` use to tell an
// empty answer apart. Reporting Python as having edges would turn "not indexed
// for edges" back into a false "none exist"; reporting an unknown name as
// indexed would hide "symbol not indexed".
func assertSymbolLanguages(t *testing.T, rs RefStorage) {
	t.Helper()
	ctx := context.Background()

	got, err := rs.SymbolLanguages(ctx, "Shared")
	if err != nil {
		t.Fatalf("SymbolLanguages: %v", err)
	}
	want := []SymbolLanguage{{Language: "go", HasEdges: true}, {Language: "py", HasEdges: false}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SymbolLanguages(Shared) = %+v, want %+v", got, want)
	}

	none, err := rs.SymbolLanguages(ctx, "Missing")
	if err != nil {
		t.Fatalf("SymbolLanguages(Missing): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("SymbolLanguages(Missing) = %+v, want none", none)
	}
}

func TestSQLiteStorage_SymbolLanguages(t *testing.T) {
	storage, cleanup := createTestSQLiteStorage(t)
	defer cleanup()
	ctx := context.Background()

	batch := make([]ChunkWithEmbedding, 0, len(symbolLanguagesChunks))
	for _, c := range symbolLanguagesChunks {
		batch = append(batch, ChunkWithEmbedding{Chunk: c, Embedding: []float32{0.1}})
	}
	if err := storage.CreateBatch(ctx, batch); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if err := storage.StoreRefs(ctx, symbolLanguagesRefs); err != nil {
		t.Fatalf("StoreRefs: %v", err)
	}

	assertSymbolLanguages(t, storage)

	if err := storage.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := storage.SymbolLanguages(ctx, "Shared"); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("SymbolLanguages on a closed store = %v, want ErrStorageClosed", err)
	}
}

func TestLexicalIndex_SymbolLanguages(t *testing.T) {
	idx, err := NewLexicalIndex(":memory:", 4)
	if err != nil {
		t.Fatalf("NewLexicalIndex: %v", err)
	}
	ctx := context.Background()

	if err := idx.IndexBatch(ctx, symbolLanguagesChunks); err != nil {
		t.Fatalf("IndexBatch: %v", err)
	}
	if err := idx.StoreRefs(ctx, symbolLanguagesRefs); err != nil {
		t.Fatalf("StoreRefs: %v", err)
	}

	assertSymbolLanguages(t, idx)

	if err := idx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := idx.SymbolLanguages(ctx, "Shared"); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("SymbolLanguages on a closed index = %v, want ErrStorageClosed", err)
	}
}

// TestQdrantStorage_SymbolLanguagesDelegateToParallelIndex: Qdrant serves refs
// from its parallel SQLite index, so the lookup has to go there too.
func TestQdrantStorage_SymbolLanguagesDelegateToParallelIndex(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "symbol-languages", 4)
	ctx := context.Background()

	for _, c := range symbolLanguagesChunks {
		if err := storage.Create(ctx, c, []float32{1, 0, 0, 0}); err != nil {
			t.Fatalf("Create %s: %v", c.ID, err)
		}
	}
	if err := storage.StoreRefs(ctx, symbolLanguagesRefs); err != nil {
		t.Fatalf("StoreRefs: %v", err)
	}

	assertSymbolLanguages(t, storage)
}
