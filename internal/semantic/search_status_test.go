package semantic

import (
	"context"
	"errors"
	"testing"
)

// TestSearchWithStatus_ReportsRerankerFailure defends against a failed reranker
// being swallowed: Search falls back to embedding scores, and without the
// status the caller cannot tell that answer from a reranked one (epic 35).
func TestSearchWithStatus_ReportsRerankerFailure(t *testing.T) {
	storage := prefilterFixture(t)
	embedder := &tableEmbedder{fallback: []float32{0.9, 0.5, 0.1, 0}}
	rerankErr := errors.New("reranker down")
	searcher := NewSearcherWithReranker(storage, embedder, &stubReranker{err: rerankErr})

	results, gotRerankErr, err := searcher.SearchWithStatus(context.Background(), "anything", SearchOptions{
		TopK: 3, Rerank: true,
	})
	if err != nil {
		t.Fatalf("SearchWithStatus() err = %v, want the fallback results and no search error", err)
	}
	if !errors.Is(gotRerankErr, rerankErr) {
		t.Errorf("rerank error = %v, want %v", gotRerankErr, rerankErr)
	}
	if len(results) == 0 {
		t.Error("the embedding fallback returned no results")
	}
}

// TestSearchWithStatus_HealthyRerankReportsNothing guards the other direction:
// a reranker that worked must not be reported as failed.
func TestSearchWithStatus_HealthyRerankReportsNothing(t *testing.T) {
	storage := prefilterFixture(t)
	embedder := &tableEmbedder{fallback: []float32{0.9, 0.5, 0.1, 0}}
	reranker := &stubReranker{scores: map[string]float32{}}
	searcher := NewSearcherWithReranker(storage, embedder, reranker)

	_, rerankErr, err := searcher.SearchWithStatus(context.Background(), "anything", SearchOptions{
		TopK: 3, Rerank: true,
	})
	if err != nil {
		t.Fatalf("SearchWithStatus() err = %v", err)
	}
	if reranker.calls != 1 {
		t.Fatalf("reranker called %d times, want 1", reranker.calls)
	}
	if rerankErr != nil {
		t.Errorf("rerank error = %v, want nil for a reranker that succeeded", rerankErr)
	}
}

// TestPrefilterSearchWithStatus_ReportsRerankerFailure defends the same status
// on the two-stage path, where lexical matching chose the candidates.
func TestPrefilterSearchWithStatus_ReportsRerankerFailure(t *testing.T) {
	storage := prefilterFixture(t)
	embedder := &tableEmbedder{fallback: []float32{0.9, 0.5, 0.1, 0}}
	rerankErr := errors.New("reranker down")
	searcher := NewSearcherWithReranker(storage, embedder, &stubReranker{err: rerankErr})

	_, gotRerankErr, err := searcher.PrefilterSearchWithStatus(context.Background(), "func", PrefilterSearchOptions{
		SearchOptions: SearchOptions{TopK: 3, Rerank: true},
	})
	if err != nil {
		t.Fatalf("PrefilterSearchWithStatus() err = %v", err)
	}
	if !errors.Is(gotRerankErr, rerankErr) {
		t.Errorf("rerank error = %v, want %v", gotRerankErr, rerankErr)
	}
}

// TestPrefilterSearchWithStatus_FallbackKeepsRerankerFailure defends the
// prefilter's fall back to a plain search when nothing matches lexically: the
// status from that inner search must not be dropped on the way out.
func TestPrefilterSearchWithStatus_FallbackKeepsRerankerFailure(t *testing.T) {
	storage := prefilterFixture(t)
	embedder := &tableEmbedder{fallback: []float32{0.9, 0.5, 0.1, 0}}
	rerankErr := errors.New("reranker down")
	searcher := NewSearcherWithReranker(storage, embedder, &stubReranker{err: rerankErr})

	_, gotRerankErr, err := searcher.PrefilterSearchWithStatus(context.Background(), "zzzunmatchedqqq", PrefilterSearchOptions{
		SearchOptions: SearchOptions{TopK: 3, Rerank: true},
	})
	if err != nil {
		t.Fatalf("PrefilterSearchWithStatus() err = %v", err)
	}
	if !errors.Is(gotRerankErr, rerankErr) {
		t.Errorf("rerank error = %v, want %v from the plain-search fallback", gotRerankErr, rerankErr)
	}
}

// TestPrefilterSearchWithStatus_HealthyRerankReportsNothing: a working
// reranker on the two-stage path reports no failure.
func TestPrefilterSearchWithStatus_HealthyRerankReportsNothing(t *testing.T) {
	storage := prefilterFixture(t)
	embedder := &tableEmbedder{fallback: []float32{0.9, 0.5, 0.1, 0}}
	reranker := &stubReranker{scores: map[string]float32{}}
	searcher := NewSearcherWithReranker(storage, embedder, reranker)

	_, rerankErr, err := searcher.PrefilterSearchWithStatus(context.Background(), "func", PrefilterSearchOptions{
		SearchOptions: SearchOptions{TopK: 3, Rerank: true},
	})
	if err != nil {
		t.Fatalf("PrefilterSearchWithStatus() err = %v", err)
	}
	if reranker.calls != 1 {
		t.Fatalf("reranker called %d times, want 1", reranker.calls)
	}
	if rerankErr != nil {
		t.Errorf("rerank error = %v, want nil for a reranker that succeeded", rerankErr)
	}
}

// TestPrefilterSearchWithStatus_LexicalErrorKeepsRerankerFailure defends the
// prefilter's other fall back to a plain search, taken when the lexical stage
// errors: an unbalanced quote is an FTS5 syntax error. The reranker status
// from that inner search must reach the caller too (EST-8769 review).
func TestPrefilterSearchWithStatus_LexicalErrorKeepsRerankerFailure(t *testing.T) {
	storage := prefilterFixture(t)
	if _, err := storage.LexicalSearch(context.Background(), `func"`, LexicalSearchOptions{TopK: 5}); err == nil {
		t.Fatal("the fixture's lexical search accepted an unbalanced quote; this test no longer reaches the lexical-error fallback")
	}
	embedder := &tableEmbedder{fallback: []float32{0.9, 0.5, 0.1, 0}}
	rerankErr := errors.New("reranker down")
	reranker := &stubReranker{err: rerankErr}
	searcher := NewSearcherWithReranker(storage, embedder, reranker)

	results, gotRerankErr, err := searcher.PrefilterSearchWithStatus(context.Background(), `func"`, PrefilterSearchOptions{
		SearchOptions: SearchOptions{TopK: 3, Rerank: true},
	})
	if err != nil {
		t.Fatalf("PrefilterSearchWithStatus() err = %v, want the plain-search fallback", err)
	}
	if reranker.calls != 1 {
		t.Fatalf("reranker called %d times, want 1 (the fallback must reach the reranker)", reranker.calls)
	}
	if len(results) == 0 {
		t.Error("the plain-search fallback returned no results")
	}
	if !errors.Is(gotRerankErr, rerankErr) {
		t.Errorf("rerank error = %v, want %v from the lexical-error fallback", gotRerankErr, rerankErr)
	}
}
