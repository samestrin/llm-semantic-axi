package semantic

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtractParentContext_JavaScriptClass pins the JS/TS branch of parent-context
// extraction: a class name is only prepended when the chunks are methods, and the
// `export` prefix must not end up in the emitted context.
func TestExtractParentContext_JavaScriptClass(t *testing.T) {
	lines := []string{
		"import { thing } from './thing';",
		"export class Widget {",
		"  spin() { return 1; }",
		"}",
	}

	methodChunks := []Chunk{{Language: "js", Type: ChunkMethod, Name: "spin"}}
	if got := extractParentContext(lines, methodChunks); got != "class Widget" {
		t.Errorf("extractParentContext() = %q, want \"class Widget\"", got)
	}

	// Every JS-family extension routes to the same extractor.
	for _, lang := range []string{"jsx", "ts", "tsx", "mts", "cts", "mjs", "cjs"} {
		chunks := []Chunk{{Language: lang, Type: ChunkMethod, Name: "spin"}}
		if got := extractParentContext(lines, chunks); got != "class Widget" {
			t.Errorf("extractParentContext(lang=%s) = %q, want \"class Widget\"", lang, got)
		}
	}

	// A top-level function is not inside a class, so no context is added.
	fnChunks := []Chunk{{Language: "js", Type: ChunkFunction, Name: "spin"}}
	if got := extractParentContext(lines, fnChunks); got != "" {
		t.Errorf("extractParentContext() for a non-method = %q, want \"\"", got)
	}

	// Methods in a file with no class declaration yield no context.
	noClass := []string{"const obj = {", "  spin() { return 1; }", "};"}
	if got := extractParentContext(noClass, methodChunks); got != "" {
		t.Errorf("extractParentContext() with no class = %q, want \"\"", got)
	}
}

// TestExtractParentContext_LanguageDispatch pins the switch: Go reports its package,
// Python reports the class with a trailing colon, an unknown language reports nothing,
// and an empty chunk list short-circuits.
func TestExtractParentContext_LanguageDispatch(t *testing.T) {
	goLines := []string{"// comment", "package semantic", "", "func F() {}"}
	if got := extractParentContext(goLines, []Chunk{{Language: "go", Type: ChunkFunction}}); got != "package semantic" {
		t.Errorf("go context = %q, want \"package semantic\"", got)
	}

	pyLines := []string{"class Widget:", "    def spin(self):", "        return 1"}
	if got := extractParentContext(pyLines, []Chunk{{Language: "py", Type: ChunkMethod}}); got != "class Widget:" {
		t.Errorf("python context = %q, want \"class Widget:\"", got)
	}

	if got := extractParentContext(pyLines, []Chunk{{Language: "rs", Type: ChunkMethod}}); got != "" {
		t.Errorf("unknown language context = %q, want \"\"", got)
	}
	if got := extractParentContext(goLines, nil); got != "" {
		t.Errorf("context for no chunks = %q, want \"\"", got)
	}
}

// TestHybridSearch_FusesDenseAndLexical pins that hybrid search returns the union of the
// dense and lexical hits: a chunk only the FTS5 stage can find still reaches the caller.
func TestHybridSearch_FusesDenseAndLexical(t *testing.T) {
	storage := prefilterFixture(t)
	// Query vector aligns with chunk a only; "lever" is lexical-only and hits chunk b.
	embedder := &tableEmbedder{fallback: []float32{1, 0, 0, 0}}
	searcher := NewSearcher(storage, embedder)

	results, err := searcher.HybridSearch(context.Background(), "lever", HybridSearchOptions{
		SearchOptions: SearchOptions{TopK: 10, Threshold: 0.5},
	})
	if err != nil {
		t.Fatalf("HybridSearch() error = %v", err)
	}

	got := map[string]bool{}
	for _, r := range results {
		got[r.Chunk.ID] = true
	}
	if !got["a"] {
		t.Error("chunk a (the dense hit) is missing from the fused results")
	}
	if !got["b"] {
		t.Error("chunk b (the lexical-only hit for \"lever\") is missing from the fused results")
	}
	if len(results) != 2 {
		t.Errorf("fused results = %v, want exactly a and b", ids(results))
	}
	if results[0].Preview == "" {
		t.Error("hybrid results should carry previews from applyRelevanceLabels")
	}
}

// TestHybridSearch_WeightedFusionRespectsAlpha pins the weighted branch: alpha weights
// the dense score, so pushing alpha toward 0 lets the lexical-only chunk outrank the
// dense-only one.
func TestHybridSearch_WeightedFusionRespectsAlpha(t *testing.T) {
	storage := prefilterFixture(t)
	embedder := &tableEmbedder{fallback: []float32{1, 0, 0, 0}}
	searcher := NewSearcher(storage, embedder)
	ctx := context.Background()

	denseHeavy, err := searcher.HybridSearch(ctx, "lever", HybridSearchOptions{
		SearchOptions: SearchOptions{TopK: 10, Threshold: 0.5},
		UseWeighted:   true,
		FusionAlpha:   1.0,
	})
	if err != nil {
		t.Fatalf("HybridSearch() error = %v", err)
	}
	if len(denseHeavy) == 0 || denseHeavy[0].Chunk.ID != "a" {
		t.Errorf("alpha=1.0 order = %v, want the dense hit a first", ids(denseHeavy))
	}

	lexHeavy, err := searcher.HybridSearch(ctx, "lever", HybridSearchOptions{
		SearchOptions: SearchOptions{TopK: 10, Threshold: 0.5},
		UseWeighted:   true,
		FusionAlpha:   0.01,
	})
	if err != nil {
		t.Fatalf("HybridSearch() error = %v", err)
	}
	if len(lexHeavy) == 0 || lexHeavy[0].Chunk.ID != "b" {
		t.Errorf("alpha=0.01 order = %v, want the lexical hit b first", ids(lexHeavy))
	}
}

// TestHybridSearch_TopKLimit pins that the fused list is trimmed to TopK.
func TestHybridSearch_TopKLimit(t *testing.T) {
	storage := prefilterFixture(t)
	searcher := NewSearcher(storage, &tableEmbedder{fallback: []float32{1, 0, 0, 0}})

	results, err := searcher.HybridSearch(context.Background(), "lever", HybridSearchOptions{
		SearchOptions: SearchOptions{TopK: 1},
	})
	if err != nil {
		t.Fatalf("HybridSearch() error = %v", err)
	}
	if len(results) != 1 {
		t.Errorf("got %d results, want 1", len(results))
	}
}

// TestHybridSearch_FallsBackOnLexicalError pins that an FTS5 syntax error degrades to
// dense-only results instead of failing the whole search.
func TestHybridSearch_FallsBackOnLexicalError(t *testing.T) {
	storage := prefilterFixture(t)
	searcher := NewSearcher(storage, &tableEmbedder{fallback: []float32{1, 0, 0, 0}})

	results, err := searcher.HybridSearch(context.Background(), `lever"`, HybridSearchOptions{
		SearchOptions: SearchOptions{TopK: 10, Threshold: 0.5},
	})
	if err != nil {
		t.Fatalf("HybridSearch() should swallow the lexical error, got %v", err)
	}
	if len(results) != 1 || results[0].Chunk.ID != "a" {
		t.Errorf("results = %v, want only the dense hit a", ids(results))
	}
}

// TestHybridSearchMemory_FusesDenseAndLexical pins the memory-side hybrid search: an
// entry only the FTS5 stage matches is still returned alongside the dense hit.
func TestHybridSearchMemory_FusesDenseAndLexical(t *testing.T) {
	storage := memoryFTSFixture(t)
	// The query vector is aligned with m1 and orthogonal to m3; "lever" only matches m3.
	embedder := &tableEmbedder{fallback: []float32{1, 0, 0, 0}}

	results, err := HybridSearchMemory(context.Background(), storage, embedder, "lever",
		HybridMemorySearchOptions{MemorySearchOptions: MemorySearchOptions{TopK: 10, Threshold: 0.5}})
	if err != nil {
		t.Fatalf("HybridSearchMemory() error = %v", err)
	}

	got := map[string]bool{}
	for _, r := range results {
		got[r.Entry.ID] = true
	}
	if !got["m1"] {
		t.Error("m1 (the dense hit) is missing from the fused memory results")
	}
	if !got["m3"] {
		t.Error("m3 (the lexical-only hit for \"lever\") is missing from the fused memory results")
	}
}

// TestHybridSearchMemory_Errors pins the two rejection paths: an empty query and a
// storage backend without memory FTS support.
func TestHybridSearchMemory_Errors(t *testing.T) {
	storage := memoryFTSFixture(t)
	embedder := &tableEmbedder{fallback: []float32{1, 0, 0, 0}}
	ctx := context.Background()

	if _, err := HybridSearchMemory(ctx, storage, embedder, "", HybridMemorySearchOptions{}); err == nil {
		t.Error("HybridSearchMemory() with an empty query should fail")
	}

	_, err := HybridSearchMemory(ctx, &noLexicalStorage{}, embedder, "q", HybridMemorySearchOptions{})
	if err == nil || !strings.Contains(err.Error(), "lexical memory search") {
		t.Errorf("error = %v, want a lexical-memory-support error", err)
	}
}

// TestHybridSearchMemory_TopKLimit pins that the fused memory list is trimmed to TopK.
func TestHybridSearchMemory_TopKLimit(t *testing.T) {
	storage := memoryFTSFixture(t)
	embedder := &tableEmbedder{fallback: []float32{1, 1, 1, 0}}

	results, err := HybridSearchMemory(context.Background(), storage, embedder, "watchdog",
		HybridMemorySearchOptions{MemorySearchOptions: MemorySearchOptions{TopK: 1}})
	if err != nil {
		t.Fatalf("HybridSearchMemory() error = %v", err)
	}
	if len(results) != 1 {
		t.Errorf("got %d results, want 1", len(results))
	}
}

// TestSQLiteStorage_ListFilters pins List's filter and paging clauses, which the CLI's
// listing commands rely on.
func TestSQLiteStorage_ListFilters(t *testing.T) {
	storage, err := NewSQLiteStorage(filepath.Join(t.TempDir(), "list.db"), 4)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer storage.Close()

	ctx := context.Background()
	seed := []Chunk{
		{ID: "1", FilePath: "a.go", Name: "A", Type: ChunkFunction, Content: "a", Language: "go"},
		{ID: "2", FilePath: "a.go", Name: "B", Type: ChunkMethod, Content: "b", Language: "go"},
		{ID: "3", FilePath: "b.py", Name: "C", Type: ChunkFunction, Content: "c", Language: "py"},
	}
	for _, c := range seed {
		if err := storage.Create(ctx, c, []float32{1, 0, 0, 0}); err != nil {
			t.Fatalf("failed to create chunk %s: %v", c.ID, err)
		}
	}

	byPath, err := storage.List(ctx, ListOptions{FilePath: "a.go"})
	if err != nil {
		t.Fatalf("List(FilePath) error = %v", err)
	}
	if len(byPath) != 2 {
		t.Errorf("FilePath filter returned %d chunks, want 2", len(byPath))
	}

	byType, err := storage.List(ctx, ListOptions{Type: ChunkMethod.String()})
	if err != nil {
		t.Fatalf("List(Type) error = %v", err)
	}
	if len(byType) != 1 || byType[0].ID != "2" {
		t.Errorf("Type filter returned %v, want just chunk 2", byType)
	}

	byLang, err := storage.List(ctx, ListOptions{Language: "py"})
	if err != nil {
		t.Fatalf("List(Language) error = %v", err)
	}
	if len(byLang) != 1 || byLang[0].ID != "3" {
		t.Errorf("Language filter returned %v, want just chunk 3", byLang)
	}

	limited, err := storage.List(ctx, ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("List(Limit) error = %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("Limit=2 returned %d chunks, want 2", len(limited))
	}

	offset, err := storage.List(ctx, ListOptions{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("List(Offset) error = %v", err)
	}
	if len(offset) != 1 {
		t.Errorf("Limit=2 Offset=2 returned %d chunks, want 1", len(offset))
	}
}
