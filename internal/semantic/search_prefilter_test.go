package semantic

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// tableEmbedder returns a different vector per query text, so tests can control the
// cosine ranking that SQLiteStorage.Search produces.
type tableEmbedder struct {
	byText   map[string][]float32
	fallback []float32
	err      error
}

func (e *tableEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	if v, ok := e.byText[text]; ok {
		return v, nil
	}
	return e.fallback, nil
}

func (e *tableEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := e.Embed(ctx, t)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (e *tableEmbedder) Dimensions() int { return 4 }
func (e *tableEmbedder) Model() string   { return "table-embedder" }

// stubReranker returns a fixed score per document text.
type stubReranker struct {
	scores map[string]float32
	err    error
	calls  int
}

func (r *stubReranker) Rerank(ctx context.Context, query string, documents []string) ([]float32, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	out := make([]float32, len(documents))
	for i, d := range documents {
		out[i] = r.scores[d]
	}
	return out, nil
}

func (r *stubReranker) Model() string { return "stub-reranker" }

// failingStorage satisfies Storage but fails every vector search. The embedded nil
// interface covers the methods these tests never reach.
type failingStorage struct {
	Storage
	err error
}

func (f *failingStorage) Search(ctx context.Context, q []float32, opts SearchOptions) ([]SearchResult, error) {
	return nil, f.err
}

func (f *failingStorage) GetCalibrationMetadata(ctx context.Context) (*CalibrationMetadata, error) {
	return nil, nil
}

// noLexicalStorage satisfies Storage but deliberately does NOT implement LexicalSearcher,
// so the hybrid/prefilter type assertions must fail.
type noLexicalStorage struct {
	Storage
}

// prefilterFixture builds a storage holding three chunks whose embeddings are the
// canonical basis vectors, so a query vector selects a known winner.
//
// The database is file-backed rather than ":memory:" because SearchMultiple issues
// concurrent queries and each pooled connection to ":memory:" would get its own empty
// database.
func prefilterFixture(t *testing.T) *SQLiteStorage {
	t.Helper()
	storage, err := NewSQLiteStorage(filepath.Join(t.TempDir(), "prefilter.db"), 4)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	ctx := context.Background()
	seed := []struct {
		chunk Chunk
		emb   []float32
	}{
		{Chunk{ID: "a", FilePath: "widget.go", Name: "Widget", Type: ChunkFunction, Content: "func Widget() { spinner() }"}, []float32{1, 0, 0, 0}},
		{Chunk{ID: "b", FilePath: "gadget.go", Name: "Gadget", Type: ChunkFunction, Content: "func Gadget() { lever() }"}, []float32{0, 1, 0, 0}},
		{Chunk{ID: "c", FilePath: "doodad.go", Name: "Doodad", Type: ChunkFunction, Content: "func Doodad() { crank() }"}, []float32{0, 0, 1, 0}},
	}
	for _, s := range seed {
		if err := storage.Create(ctx, s.chunk, s.emb); err != nil {
			t.Fatalf("failed to create chunk %s: %v", s.chunk.ID, err)
		}
	}
	return storage
}

// TestPrefilterSearch_NarrowsToLexicalCandidates pins the two-stage contract: only chunks
// the FTS5 stage matched may reach the vector stage. Without the ChunkIDs handoff all
// three seeded chunks would come back.
func TestPrefilterSearch_NarrowsToLexicalCandidates(t *testing.T) {
	storage := prefilterFixture(t)
	embedder := &tableEmbedder{fallback: []float32{1, 1, 1, 0}}
	searcher := NewSearcher(storage, embedder)

	results, err := searcher.PrefilterSearch(context.Background(), "spinner", PrefilterSearchOptions{
		SearchOptions: SearchOptions{TopK: 10},
	})
	if err != nil {
		t.Fatalf("PrefilterSearch() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("PrefilterSearch() returned %d results, want 1 (only the lexical candidate)", len(results))
	}
	if results[0].Chunk.ID != "a" {
		t.Errorf("result = %q, want chunk a (the only chunk containing \"spinner\")", results[0].Chunk.ID)
	}
	if results[0].Preview == "" {
		t.Error("prefilter results should carry a preview from applyRelevanceLabels")
	}
}

// TestPrefilterSearch_FallsBackWhenNoLexicalMatch pins the fallback: a query the FTS5
// index cannot match must degrade to a plain vector search over the whole index rather
// than returning nothing.
func TestPrefilterSearch_FallsBackWhenNoLexicalMatch(t *testing.T) {
	storage := prefilterFixture(t)
	embedder := &tableEmbedder{fallback: []float32{1, 1, 1, 0}}
	searcher := NewSearcher(storage, embedder)

	results, err := searcher.PrefilterSearch(context.Background(), "nonexistentterm", PrefilterSearchOptions{
		SearchOptions: SearchOptions{TopK: 10},
	})
	if err != nil {
		t.Fatalf("PrefilterSearch() error = %v", err)
	}
	if len(results) != 3 {
		t.Errorf("PrefilterSearch() with no lexical match returned %d results, want all 3 via fallback", len(results))
	}
}

// TestPrefilterSearch_FallsBackOnLexicalError pins that an FTS5 syntax error in stage one
// degrades to a plain search instead of surfacing the SQL error to the caller.
func TestPrefilterSearch_FallsBackOnLexicalError(t *testing.T) {
	storage := prefilterFixture(t)
	embedder := &tableEmbedder{fallback: []float32{1, 1, 1, 0}}
	searcher := NewSearcher(storage, embedder)

	// An unbalanced quote is an FTS5 MATCH syntax error.
	results, err := searcher.PrefilterSearch(context.Background(), `widget"`, PrefilterSearchOptions{
		SearchOptions: SearchOptions{TopK: 10},
	})
	if err != nil {
		t.Fatalf("PrefilterSearch() should swallow the lexical error, got %v", err)
	}
	if len(results) != 3 {
		t.Errorf("PrefilterSearch() after a lexical error returned %d results, want all 3 via fallback", len(results))
	}
}

// TestPrefilterSearch_RequiresLexicalStorage pins the capability check: a backend without
// FTS5 must be rejected by name, not silently treated as an empty candidate set.
func TestPrefilterSearch_RequiresLexicalStorage(t *testing.T) {
	searcher := NewSearcher(&noLexicalStorage{}, &tableEmbedder{fallback: []float32{1, 0, 0, 0}})

	_, err := searcher.PrefilterSearch(context.Background(), "q", PrefilterSearchOptions{})
	if err == nil {
		t.Fatal("PrefilterSearch() on a non-lexical backend should error")
	}
	if !strings.Contains(err.Error(), "lexical search support") {
		t.Errorf("error = %v, want it to mention lexical search support", err)
	}
}

// TestPrefilterSearch_Validation pins the argument guards, which run before any storage
// or embedder call.
func TestPrefilterSearch_Validation(t *testing.T) {
	storage := prefilterFixture(t)
	searcher := NewSearcher(storage, &tableEmbedder{err: errors.New("embedder must not be called")})

	tests := []struct {
		name string
		q    string
		opts PrefilterSearchOptions
		want string
	}{
		{"empty query", "", PrefilterSearchOptions{}, "query cannot be empty"},
		{"threshold too high", "q", PrefilterSearchOptions{SearchOptions: SearchOptions{Threshold: 1.5}}, "threshold must be between"},
		{"negative threshold", "q", PrefilterSearchOptions{SearchOptions: SearchOptions{Threshold: -0.1}}, "threshold must be between"},
		{"negative topK", "q", PrefilterSearchOptions{SearchOptions: SearchOptions{TopK: -1}}, "topK must be non-negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := searcher.PrefilterSearch(context.Background(), tt.q, tt.opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("PrefilterSearch() error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

// TestPrefilterSearch_AppliesReranking pins that stage two honours the reranker: scores
// are replaced by the reranker's and the RerankThreshold drops low-scoring candidates.
func TestPrefilterSearch_AppliesReranking(t *testing.T) {
	storage := prefilterFixture(t)
	embedder := &tableEmbedder{fallback: []float32{1, 1, 1, 0}}
	reranker := &stubReranker{scores: map[string]float32{
		"func Widget() { spinner() }": 0.9,
	}}
	searcher := NewSearcherWithReranker(storage, embedder, reranker)

	results, err := searcher.PrefilterSearch(context.Background(), "spinner", PrefilterSearchOptions{
		SearchOptions: SearchOptions{TopK: 5, Rerank: true, RerankThreshold: 0.5},
	})
	if err != nil {
		t.Fatalf("PrefilterSearch() error = %v", err)
	}
	if reranker.calls != 1 {
		t.Fatalf("reranker called %d times, want 1", reranker.calls)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Score != 0.9 {
		t.Errorf("score = %v, want the reranker score 0.9", results[0].Score)
	}
}

// TestSearcher_RerankerWiring pins the three accessors that make reranking optional:
// a plain Searcher has none, the with-reranker constructor installs one, and SetReranker
// can both install and clear it.
func TestSearcher_RerankerWiring(t *testing.T) {
	storage := prefilterFixture(t)
	embedder := &tableEmbedder{fallback: []float32{1, 0, 0, 0}}
	reranker := &stubReranker{}

	plain := NewSearcher(storage, embedder)
	if plain.HasReranker() {
		t.Error("NewSearcher() should not install a reranker")
	}

	withRR := NewSearcherWithReranker(storage, embedder, reranker)
	if !withRR.HasReranker() {
		t.Error("NewSearcherWithReranker() should install a reranker")
	}

	plain.SetReranker(reranker)
	if !plain.HasReranker() {
		t.Error("SetReranker() should install a reranker")
	}
	plain.SetReranker(nil)
	if plain.HasReranker() {
		t.Error("SetReranker(nil) should clear the reranker")
	}
}

// TestSearch_RerankingReordersResults pins that reranking overrides the embedding order:
// the chunk the vector stage ranked last comes first once the reranker scores it highest.
func TestSearch_RerankingReordersResults(t *testing.T) {
	storage := prefilterFixture(t)
	// Query vector favours chunk a, then b, then c on cosine score.
	embedder := &tableEmbedder{fallback: []float32{0.9, 0.5, 0.1, 0}}
	reranker := &stubReranker{scores: map[string]float32{
		"func Widget() { spinner() }": 0.1,
		"func Gadget() { lever() }":   0.4,
		"func Doodad() { crank() }":   0.95,
	}}
	searcher := NewSearcherWithReranker(storage, embedder, reranker)
	ctx := context.Background()

	baseline, err := searcher.Search(ctx, "anything", SearchOptions{TopK: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(baseline) != 3 || baseline[0].Chunk.ID != "a" {
		t.Fatalf("baseline order = %v, want chunk a first", ids(baseline))
	}

	reranked, err := searcher.Search(ctx, "anything", SearchOptions{TopK: 3, Rerank: true})
	if err != nil {
		t.Fatalf("Search() with rerank error = %v", err)
	}
	got := ids(reranked)
	want := []string{"c", "b", "a"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("reranked order = %v, want %v", got, want)
		}
	}
	if reranked[0].Score != 0.95 {
		t.Errorf("top score = %v, want the reranker score 0.95", reranked[0].Score)
	}
}

// TestSearch_RerankCandidatesCapsOverFetch pins that an explicit RerankCandidates value
// caps the candidate pool: the vector stage fetches only that many and the post-rerank
// trim keeps that many, so the reranker reorders exactly the candidates it was given.
func TestSearch_RerankCandidatesCapsOverFetch(t *testing.T) {
	storage := prefilterFixture(t)
	// Query vector orders the chunks a > b > c on cosine score.
	embedder := &tableEmbedder{fallback: []float32{0.9, 0.5, 0.1, 0}}
	reranker := &stubReranker{scores: map[string]float32{
		"func Widget() { spinner() }": 0.3,
		"func Gadget() { lever() }":   0.8,
		"func Doodad() { crank() }":   0.5,
	}}
	searcher := NewSearcherWithReranker(storage, embedder, reranker)

	results, err := searcher.Search(context.Background(), "anything", SearchOptions{
		TopK: 2, Rerank: true, RerankCandidates: 2,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (RerankCandidates caps the pool)", len(results))
	}
	// c never entered the pool, so it cannot appear despite the highest-but-one rerank score.
	if results[0].Chunk.ID != "b" || results[1].Chunk.ID != "a" {
		t.Errorf("order = %v, want [b a]: only a and b were candidates, reordered by the reranker", ids(results))
	}
}

// TestSearch_RerankingIgnoresCallerTopK documents a defect: when reranking is on, Search
// overwrites searchOpts.TopK with the over-fetch candidate count and then passes those
// same options to applyReranking, so the caller's TopK is never re-applied. A TopK of 2
// therefore yields every candidate the reranker scored.
func TestSearch_RerankingIgnoresCallerTopK(t *testing.T) {
	storage := prefilterFixture(t)
	embedder := &tableEmbedder{fallback: []float32{0.9, 0.5, 0.1, 0}}
	reranker := &stubReranker{scores: map[string]float32{
		"func Widget() { spinner() }": 0.3,
		"func Gadget() { lever() }":   0.8,
		"func Doodad() { crank() }":   0.5,
	}}
	searcher := NewSearcherWithReranker(storage, embedder, reranker)

	results, err := searcher.Search(context.Background(), "anything", SearchOptions{TopK: 2, Rerank: true})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3 - the caller's TopK=2 is currently dropped when reranking", len(results))
	}
	if results[0].Chunk.ID != "b" {
		t.Errorf("order = %v, want b first by reranker score", ids(results))
	}
}

// TestSearch_RerankerErrorFallsBackToEmbeddingScores pins the resilience path: a reranker
// failure is logged and the embedding-scored results are returned, not an error.
func TestSearch_RerankerErrorFallsBackToEmbeddingScores(t *testing.T) {
	storage := prefilterFixture(t)
	embedder := &tableEmbedder{fallback: []float32{0.9, 0.5, 0.1, 0}}
	reranker := &stubReranker{err: errors.New("reranker down")}
	searcher := NewSearcherWithReranker(storage, embedder, reranker)

	results, err := searcher.Search(context.Background(), "anything", SearchOptions{TopK: 3, Rerank: true})
	if err != nil {
		t.Fatalf("Search() should not surface a reranker failure, got %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	if results[0].Chunk.ID != "a" {
		t.Errorf("order = %v, want the embedding order [a b c] preserved", ids(results))
	}
}

// TestSearchMultiple_DedupesKeepingHighestScore pins the merge: a chunk hit by several
// queries appears once, carrying the best score any query gave it.
func TestSearchMultiple_DedupesKeepingHighestScore(t *testing.T) {
	storage := prefilterFixture(t)
	embedder := &tableEmbedder{byText: map[string][]float32{
		"first":  {1, 0, 0, 0}, // perfectly matches chunk a
		"second": {0, 1, 0, 0}, // perfectly matches chunk b
	}}
	searcher := NewSearcher(storage, embedder)

	results, err := searcher.SearchMultiple(context.Background(), []string{"first", "second"}, SearchOptions{TopK: 10})
	if err != nil {
		t.Fatalf("SearchMultiple() error = %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("SearchMultiple() returned %d results, want 3 deduped chunks, got %v", len(results), ids(results))
	}

	byID := map[string]float32{}
	for _, r := range results {
		if _, dup := byID[r.Chunk.ID]; dup {
			t.Fatalf("chunk %s appeared twice; results must be deduped", r.Chunk.ID)
		}
		byID[r.Chunk.ID] = r.Score
	}
	if byID["a"] < 0.99 {
		t.Errorf("chunk a score = %v, want ~1.0 (best score across queries kept)", byID["a"])
	}
	if byID["b"] < 0.99 {
		t.Errorf("chunk b score = %v, want ~1.0 (best score across queries kept)", byID["b"])
	}
	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Errorf("results not sorted by score: %v", results)
		}
	}
}

// TestSearchMultiple_TopKLimit pins that the limit is applied after the merge, not per
// query, so a two-query search still honours TopK=1.
func TestSearchMultiple_TopKLimit(t *testing.T) {
	storage := prefilterFixture(t)
	embedder := &tableEmbedder{byText: map[string][]float32{
		"first":  {1, 0, 0, 0},
		"second": {0, 1, 0, 0},
	}}
	searcher := NewSearcher(storage, embedder)

	results, err := searcher.SearchMultiple(context.Background(), []string{"first", "second"}, SearchOptions{TopK: 1})
	if err != nil {
		t.Fatalf("SearchMultiple() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Score < 0.99 {
		t.Errorf("kept result score = %v, want the top-scoring chunk", results[0].Score)
	}
}

// TestSearchMultiple_Validation pins the argument guards, including the index reported
// for a blank query inside an otherwise valid list.
func TestSearchMultiple_Validation(t *testing.T) {
	storage := prefilterFixture(t)
	searcher := NewSearcher(storage, &tableEmbedder{fallback: []float32{1, 0, 0, 0}})
	ctx := context.Background()

	if _, err := searcher.SearchMultiple(ctx, nil, SearchOptions{}); err == nil ||
		!strings.Contains(err.Error(), "queries cannot be empty") {
		t.Errorf("empty query list error = %v, want \"queries cannot be empty\"", err)
	}
	_, err := searcher.SearchMultiple(ctx, []string{"ok", ""}, SearchOptions{})
	if err == nil || !strings.Contains(err.Error(), "index 1") {
		t.Errorf("blank query error = %v, want it to name index 1", err)
	}
	if _, err := searcher.SearchMultiple(ctx, []string{"ok"}, SearchOptions{Threshold: 2}); err == nil ||
		!strings.Contains(err.Error(), "threshold must be between") {
		t.Errorf("threshold error = %v", err)
	}
	if _, err := searcher.SearchMultiple(ctx, []string{"ok"}, SearchOptions{TopK: -3}); err == nil ||
		!strings.Contains(err.Error(), "topK must be non-negative") {
		t.Errorf("topK error = %v", err)
	}
}

// TestSearchMultiple_EmbedderError pins that a batch-embedding failure aborts the search
// rather than silently searching with fewer queries.
func TestSearchMultiple_EmbedderError(t *testing.T) {
	storage := prefilterFixture(t)
	searcher := NewSearcher(storage, &tableEmbedder{err: errors.New("embed boom")})

	_, err := searcher.SearchMultiple(context.Background(), []string{"a", "b"}, SearchOptions{})
	if err == nil || !strings.Contains(err.Error(), "embed boom") {
		t.Errorf("SearchMultiple() error = %v, want the embedder error", err)
	}
}

// TestSearchMultiple_StorageErrorsAreJoined pins that per-query storage failures are
// collected and returned together instead of being dropped by the goroutines.
func TestSearchMultiple_StorageErrorsAreJoined(t *testing.T) {
	storage := &failingStorage{err: errors.New("backend offline")}
	searcher := NewSearcher(storage, &tableEmbedder{fallback: []float32{1, 0, 0, 0}})

	_, err := searcher.SearchMultiple(context.Background(), []string{"one", "two"}, SearchOptions{})
	if err == nil {
		t.Fatal("SearchMultiple() should fail when every storage search fails")
	}
	if strings.Count(err.Error(), "backend offline") != 2 {
		t.Errorf("error = %v, want both query failures joined", err)
	}
}

// TestHybridSearch_RequiresLexicalStorage pins the hybrid capability check, the sibling
// of the prefilter one.
func TestHybridSearch_RequiresLexicalStorage(t *testing.T) {
	searcher := NewSearcher(&noLexicalStorage{}, &tableEmbedder{fallback: []float32{1, 0, 0, 0}})

	_, err := searcher.HybridSearch(context.Background(), "q", HybridSearchOptions{})
	if err == nil || !strings.Contains(err.Error(), "lexical search support") {
		t.Errorf("HybridSearch() error = %v, want a lexical-support error", err)
	}
}

// ids extracts chunk ids in result order for readable failure messages.
func ids(results []SearchResult) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.Chunk.ID
	}
	return out
}
