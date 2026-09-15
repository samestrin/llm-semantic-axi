package semantic

import (
	"context"
	"errors"
	"net"
	"testing"
)

// netFailEmbedder always fails with a network error, forcing the offline fallback.
type netFailEmbedder struct{}

func (netFailEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
}

func (netFailEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
}

func (netFailEmbedder) Dimensions() int { return 4 }
func (netFailEmbedder) Model() string   { return "net-fail" }

// TestOfflineSearcher_OnlineModeDoesNotBoost pins that with a working embedder the
// searcher is not offline and returns the storage scores untouched.
func TestOfflineSearcher_OnlineModeDoesNotBoost(t *testing.T) {
	storage := &scriptedStorage{perQuery: fixedResults(
		chunkResult("x", "the spinner turns the widget", 0.40),
	)}
	embedder := NewOfflineEmbedder(&tableEmbedder{fallback: []float32{1, 0, 0, 0}}, 4)
	searcher := NewOfflineSearcher(storage, embedder)

	results, err := searcher.Search(context.Background(), "spinner widget", SearchOptions{TopK: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if searcher.IsOffline() {
		t.Error("IsOffline() = true with a healthy embedder, want false")
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Score != 0.40 {
		t.Errorf("Score = %v, want the unboosted 0.40", results[0].Score)
	}
}

// TestOfflineSearcher_OfflineBoostsKeywordMatches pins the offline ranking aid: once the
// embedder has failed with a network error, results whose content or name contains the
// query words get up to a 20% score boost, proportional to how many words matched.
func TestOfflineSearcher_OfflineBoostsKeywordMatches(t *testing.T) {
	storage := &scriptedStorage{perQuery: fixedResults(
		SearchResult{Chunk: Chunk{ID: "both", Name: "Widget", Content: "the spinner turns"}, Score: 0.40},
		SearchResult{Chunk: Chunk{ID: "one", Name: "Other", Content: "the spinner idles"}, Score: 0.40},
		SearchResult{Chunk: Chunk{ID: "none", Name: "Other", Content: "unrelated text"}, Score: 0.40},
	)}
	searcher := NewOfflineSearcher(storage, NewOfflineEmbedder(netFailEmbedder{}, 4))

	results, err := searcher.Search(context.Background(), "spinner widget", SearchOptions{TopK: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !searcher.IsOffline() {
		t.Fatal("IsOffline() = false after a network failure, want true")
	}

	scores := map[string]float32{}
	for _, r := range results {
		scores[r.Chunk.ID] = r.Score
	}
	// Two of two query words matched: 0.40 + 1.0*0.2.
	if scores["both"] < 0.599 || scores["both"] > 0.601 {
		t.Errorf("both-words score = %v, want ~0.60", scores["both"])
	}
	// One of two matched: 0.40 + 0.5*0.2.
	if scores["one"] < 0.499 || scores["one"] > 0.501 {
		t.Errorf("one-word score = %v, want ~0.50", scores["one"])
	}
	// No words matched: unchanged.
	if scores["none"] != 0.40 {
		t.Errorf("no-match score = %v, want the unboosted 0.40", scores["none"])
	}
}

// TestOfflineSearcher_BoostClampsToOne pins the upper clamp: a near-perfect score plus a
// keyword boost must not exceed 1.0.
func TestOfflineSearcher_BoostClampsToOne(t *testing.T) {
	storage := &scriptedStorage{perQuery: fixedResults(
		SearchResult{Chunk: Chunk{ID: "hot", Name: "Widget", Content: "spinner"}, Score: 0.95},
	)}
	searcher := NewOfflineSearcher(storage, NewOfflineEmbedder(netFailEmbedder{}, 4))

	results, err := searcher.Search(context.Background(), "spinner widget", SearchOptions{TopK: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if results[0].Score != 1.0 {
		t.Errorf("Score = %v, want it clamped to 1.0", results[0].Score)
	}
}

// TestOfflineSearcher_IgnoresSingleCharacterWords pins that one-character query tokens do
// not count toward the boost, so "a widget" is scored on "widget" alone - but still
// divides by the full query word count.
func TestOfflineSearcher_IgnoresSingleCharacterWords(t *testing.T) {
	storage := &scriptedStorage{perQuery: fixedResults(
		SearchResult{Chunk: Chunk{ID: "x", Name: "Widget", Content: "a a a a"}, Score: 0.40},
	)}
	searcher := NewOfflineSearcher(storage, NewOfflineEmbedder(netFailEmbedder{}, 4))

	results, err := searcher.Search(context.Background(), "a widget", SearchOptions{TopK: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	// Only "widget" counts as a match, out of two query words: 0.40 + 0.5*0.2.
	if results[0].Score < 0.499 || results[0].Score > 0.501 {
		t.Errorf("Score = %v, want ~0.50 (the single-character token is skipped)", results[0].Score)
	}
}

// TestOfflineSearcher_StorageError pins that a storage failure is wrapped and surfaced
// rather than swallowed into an empty result set.
func TestOfflineSearcher_StorageError(t *testing.T) {
	storage := &scriptedStorage{err: errors.New("backend offline")}
	searcher := NewOfflineSearcher(storage, NewOfflineEmbedder(&tableEmbedder{fallback: []float32{1, 0, 0, 0}}, 4))

	_, err := searcher.Search(context.Background(), "q", SearchOptions{})
	if err == nil {
		t.Fatal("Search() should surface the storage error")
	}
	if got := err.Error(); got != "search failed: backend offline" {
		t.Errorf("error = %q, want it wrapped as \"search failed: ...\"", got)
	}
}

// TestOfflineSearcher_EmbedderError pins that a non-network embedder failure aborts the
// search: it is a real error, not a reason to fall back to keyword embeddings.
func TestOfflineSearcher_EmbedderError(t *testing.T) {
	storage := &scriptedStorage{perQuery: fixedResults(chunkResult("x", "content", 0.5))}
	searcher := NewOfflineSearcher(storage, NewOfflineEmbedder(&tableEmbedder{err: errors.New("bad request")}, 4))

	_, err := searcher.Search(context.Background(), "q", SearchOptions{})
	if err == nil {
		t.Fatal("Search() should surface a non-network embedder error")
	}
	if got := err.Error(); got != "failed to embed query: bad request" {
		t.Errorf("error = %q, want it wrapped as \"failed to embed query: ...\"", got)
	}
}
