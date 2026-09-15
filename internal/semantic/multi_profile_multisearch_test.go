package semantic

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// scriptedStorage satisfies Storage and returns a caller-supplied result set per query
// embedding. Each call returns a fresh copy so callers that tag Chunk.Domain in place
// cannot corrupt the fixture (and so the copies stay race-free under parallel search).
type scriptedStorage struct {
	Storage
	perQuery func(emb []float32) []SearchResult
	err      error
}

func (s *scriptedStorage) Search(ctx context.Context, emb []float32, opts SearchOptions) ([]SearchResult, error) {
	if s.err != nil {
		return nil, s.err
	}
	src := s.perQuery(emb)
	out := make([]SearchResult, len(src))
	copy(out, src)
	return out, nil
}

func (s *scriptedStorage) GetCalibrationMetadata(ctx context.Context) (*CalibrationMetadata, error) {
	return nil, nil
}

// fixedResults builds a perQuery function that ignores the embedding.
func fixedResults(results ...SearchResult) func([]float32) []SearchResult {
	return func([]float32) []SearchResult { return results }
}

func chunkResult(id, content string, score float32) SearchResult {
	return SearchResult{
		Chunk: Chunk{ID: id, Name: id, FilePath: id + ".go", Type: ChunkFunction, Content: content},
		Score: score,
	}
}

// twoProfileSearcher wires a code and a docs profile over scripted storages.
func twoProfileSearcher(code, docs Storage, embedder EmbedderInterface) *MultiProfileSearcher {
	return NewMultiProfileSearcher(embedder, map[string]Storage{"code": code, "docs": docs})
}

// TestMultiProfileSearcher_GetDefaultProfile pins the default profile accessor and that
// SetDefaultProfile is what Search falls back to when no profiles are requested.
func TestMultiProfileSearcher_GetDefaultProfile(t *testing.T) {
	code := &scriptedStorage{perQuery: fixedResults(chunkResult("x", "code chunk", 0.9))}
	docs := &scriptedStorage{perQuery: fixedResults(chunkResult("y", "docs chunk", 0.8))}
	mps := twoProfileSearcher(code, docs, &tableEmbedder{fallback: []float32{1, 0, 0, 0}})

	if got := mps.GetDefaultProfile(); got != "code" {
		t.Errorf("GetDefaultProfile() = %q, want \"code\"", got)
	}

	mps.SetDefaultProfile("docs")
	if got := mps.GetDefaultProfile(); got != "docs" {
		t.Errorf("GetDefaultProfile() after SetDefaultProfile = %q, want \"docs\"", got)
	}

	// With no explicit profiles the new default must be the one searched.
	results, err := mps.Search(context.Background(), "q", SearchOptions{TopK: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].Chunk.ID != "y" {
		t.Errorf("Search() with no profiles returned %v, want the docs chunk y", ids(results))
	}
	if results[0].Chunk.Domain != "docs" {
		t.Errorf("Domain = %q, want results tagged with the searched profile", results[0].Chunk.Domain)
	}
}

// TestMultiProfileSearcher_Multisearch_MergesQueriesAndProfiles pins the core merge:
// every query runs against every profile, results are tagged with their profile,
// matched-query lists are accumulated, and QueriesMatched counts the hits per query.
func TestMultiProfileSearcher_Multisearch_MergesQueriesAndProfiles(t *testing.T) {
	code := &scriptedStorage{perQuery: fixedResults(chunkResult("x", "code chunk", 0.90))}
	docs := &scriptedStorage{perQuery: fixedResults(chunkResult("y", "docs chunk", 0.80))}
	mps := twoProfileSearcher(code, docs, &tableEmbedder{fallback: []float32{1, 0, 0, 0}})

	res, err := mps.Multisearch(context.Background(), MultisearchOptions{
		Queries:  []string{"alpha", "beta"},
		Profiles: []string{"code", "docs"},
	})
	if err != nil {
		t.Fatalf("Multisearch() error = %v", err)
	}

	if res.TotalQueries != 2 {
		t.Errorf("TotalQueries = %d, want 2", res.TotalQueries)
	}
	if res.TotalResults != 2 {
		t.Fatalf("TotalResults = %d, want 2 deduped chunks", res.TotalResults)
	}
	// Each query hit one result in each of the two profiles.
	for _, q := range []string{"alpha", "beta"} {
		if res.QueriesMatched[q] != 2 {
			t.Errorf("QueriesMatched[%q] = %d, want 2", q, res.QueriesMatched[q])
		}
	}

	byID := map[string]EnhancedResult{}
	for _, r := range res.Results {
		byID[r.Chunk.ID] = r
	}
	x, ok := byID["x"]
	if !ok {
		t.Fatal("chunk x missing from results")
	}
	if x.Chunk.Domain != "code" {
		t.Errorf("chunk x Domain = %q, want \"code\"", x.Chunk.Domain)
	}
	if len(x.MatchedQueries) != 2 || x.MatchedQueries[0] != "alpha" || x.MatchedQueries[1] != "beta" {
		t.Errorf("chunk x MatchedQueries = %v, want [alpha beta] in query order", x.MatchedQueries)
	}
	// Boost adds BoostFactor per additional match: 0.90 + 0.05.
	if x.BoostedScore < 0.949 || x.BoostedScore > 0.951 {
		t.Errorf("chunk x BoostedScore = %v, want ~0.95", x.BoostedScore)
	}
	if byID["y"].Chunk.Domain != "docs" {
		t.Errorf("chunk y Domain = %q, want \"docs\"", byID["y"].Chunk.Domain)
	}
	if res.Results[0].Chunk.ID != "x" {
		t.Errorf("results not sorted by boosted score: %v", res.Results[0].Chunk.ID)
	}
	if res.Results[0].Relevance == "" {
		t.Error("percentile relevance labels should be applied to multisearch results")
	}
}

// TestMultiProfileSearcher_Multisearch_KeepsHighestScore pins the dedupe rule: when two
// queries return the same chunk, the higher-scoring copy survives.
func TestMultiProfileSearcher_Multisearch_KeepsHighestScore(t *testing.T) {
	// The first query's embedding is {1,...}; the second's is {2,...}.
	perQuery := func(emb []float32) []SearchResult {
		if emb[0] == 1 {
			return []SearchResult{chunkResult("x", "shared chunk", 0.20)}
		}
		return []SearchResult{chunkResult("x", "shared chunk", 0.75)}
	}
	code := &scriptedStorage{perQuery: perQuery}
	docs := &scriptedStorage{perQuery: func([]float32) []SearchResult { return nil }}
	embedder := &tableEmbedder{byText: map[string][]float32{
		"low":  {1, 0, 0, 0},
		"high": {2, 0, 0, 0},
	}}
	mps := twoProfileSearcher(code, docs, embedder)

	res, err := mps.Multisearch(context.Background(), MultisearchOptions{
		Queries:  []string{"low", "high"},
		Profiles: []string{"code"},
	})
	if err != nil {
		t.Fatalf("Multisearch() error = %v", err)
	}
	if len(res.Results) != 1 {
		t.Fatalf("got %d results, want 1 deduped chunk", len(res.Results))
	}
	if res.Results[0].Score != 0.75 {
		t.Errorf("Score = %v, want 0.75 (the higher of the two query scores)", res.Results[0].Score)
	}
	if len(res.Results[0].MatchedQueries) != 2 {
		t.Errorf("MatchedQueries = %v, want both queries recorded", res.Results[0].MatchedQueries)
	}
}

// TestMultiProfileSearcher_Multisearch_BoostDisabled pins that BoostMultiMatch=false
// leaves BoostedScore equal to the raw score even for a multi-query match.
func TestMultiProfileSearcher_Multisearch_BoostDisabled(t *testing.T) {
	code := &scriptedStorage{perQuery: fixedResults(chunkResult("x", "code chunk", 0.40))}
	mps := NewMultiProfileSearcher(&tableEmbedder{fallback: []float32{1, 0, 0, 0}},
		map[string]Storage{"code": code})

	off := false
	res, err := mps.Multisearch(context.Background(), MultisearchOptions{
		Queries:         []string{"alpha", "beta"},
		BoostMultiMatch: &off,
	})
	if err != nil {
		t.Fatalf("Multisearch() error = %v", err)
	}
	if len(res.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(res.Results))
	}
	if res.Results[0].BoostedScore != 0.40 {
		t.Errorf("BoostedScore = %v, want the unboosted 0.40", res.Results[0].BoostedScore)
	}
}

// TestMultiProfileSearcher_Multisearch_TopK pins that the limit is applied after the
// cross-profile merge and boosting, keeping the highest boosted score.
func TestMultiProfileSearcher_Multisearch_TopK(t *testing.T) {
	code := &scriptedStorage{perQuery: fixedResults(
		chunkResult("x", "code chunk", 0.90),
		chunkResult("z", "other chunk", 0.10),
	)}
	mps := NewMultiProfileSearcher(&tableEmbedder{fallback: []float32{1, 0, 0, 0}},
		map[string]Storage{"code": code})

	res, err := mps.Multisearch(context.Background(), MultisearchOptions{
		Queries: []string{"alpha"},
		TopK:    1,
	})
	if err != nil {
		t.Fatalf("Multisearch() error = %v", err)
	}
	if len(res.Results) != 1 || res.Results[0].Chunk.ID != "x" {
		t.Fatalf("results = %v, want only the top-scoring chunk x", res.Results)
	}
	if res.TotalResults != 1 {
		t.Errorf("TotalResults = %d, want 1 (counted after the TopK trim)", res.TotalResults)
	}
}

// TestMultiProfileSearcher_Multisearch_Errors pins the rejection paths: option validation
// is delegated to MultisearchOptions.Validate, unknown profile names are named in the
// error, and the profile count is capped at ten.
func TestMultiProfileSearcher_Multisearch_Errors(t *testing.T) {
	code := &scriptedStorage{perQuery: fixedResults(chunkResult("x", "code chunk", 0.9))}
	mps := NewMultiProfileSearcher(&tableEmbedder{fallback: []float32{1, 0, 0, 0}},
		map[string]Storage{"code": code})
	ctx := context.Background()

	if _, err := mps.Multisearch(ctx, MultisearchOptions{}); err == nil ||
		!strings.Contains(err.Error(), "queries cannot be empty") {
		t.Errorf("empty queries error = %v, want the Validate() error", err)
	}

	_, err := mps.Multisearch(ctx, MultisearchOptions{
		Queries:  []string{"q"},
		Profiles: []string{"nope"},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown profile: 'nope'") {
		t.Errorf("unknown profile error = %v, want it to name 'nope'", err)
	}

	many := make([]string, 11)
	for i := range many {
		many[i] = "code"
	}
	_, err = mps.Multisearch(ctx, MultisearchOptions{Queries: []string{"q"}, Profiles: many})
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum of 10") {
		t.Errorf("profile count error = %v, want a maximum-of-10 error", err)
	}
}

// TestMultiProfileSearcher_Multisearch_EmbedderError pins that a batch embedding failure
// aborts the multisearch with a wrapped error rather than returning empty results.
func TestMultiProfileSearcher_Multisearch_EmbedderError(t *testing.T) {
	code := &scriptedStorage{perQuery: fixedResults(chunkResult("x", "code chunk", 0.9))}
	mps := NewMultiProfileSearcher(&tableEmbedder{err: errors.New("embed boom")},
		map[string]Storage{"code": code})

	_, err := mps.Multisearch(context.Background(), MultisearchOptions{Queries: []string{"q"}})
	if err == nil || !strings.Contains(err.Error(), "embed boom") {
		t.Errorf("Multisearch() error = %v, want the wrapped embedder error", err)
	}
	if err != nil && !strings.Contains(err.Error(), "failed to generate embeddings") {
		t.Errorf("error = %v, want it wrapped with context", err)
	}
}

// TestMultiProfileSearcher_Multisearch_AllProfilesFail pins that a per-profile search
// error is tolerated for partial results, but a total failure is reported rather than
// returning an empty result set that looks like "no matches".
func TestMultiProfileSearcher_Multisearch_AllProfilesFail(t *testing.T) {
	broken := &scriptedStorage{err: errors.New("backend offline")}
	mps := NewMultiProfileSearcher(&tableEmbedder{fallback: []float32{1, 0, 0, 0}},
		map[string]Storage{"code": broken})

	_, err := mps.Multisearch(context.Background(), MultisearchOptions{Queries: []string{"q"}})
	if err == nil || !strings.Contains(err.Error(), "all queries failed") {
		t.Errorf("Multisearch() error = %v, want an all-queries-failed error", err)
	}
}

// TestMultiProfileSearcher_Multisearch_PartialFailure pins that one broken profile does
// not sink the search: the healthy profile's results are still returned.
func TestMultiProfileSearcher_Multisearch_PartialFailure(t *testing.T) {
	code := &scriptedStorage{perQuery: fixedResults(chunkResult("x", "code chunk", 0.9))}
	docs := &scriptedStorage{err: errors.New("docs offline")}
	mps := twoProfileSearcher(code, docs, &tableEmbedder{fallback: []float32{1, 0, 0, 0}})

	res, err := mps.Multisearch(context.Background(), MultisearchOptions{
		Queries:  []string{"q"},
		Profiles: []string{"code", "docs"},
	})
	if err != nil {
		t.Fatalf("Multisearch() should tolerate one failed profile, got %v", err)
	}
	if len(res.Results) != 1 || res.Results[0].Chunk.ID != "x" {
		t.Errorf("results = %v, want the healthy profile's chunk x", res.Results)
	}
}
