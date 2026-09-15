package semantic

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunCalibration_EndToEnd pins the calibration pipeline against a real index: the
// self-match probe scores a chunk against itself (1.0 with a per-text embedder), the
// unrelated probe establishes a lower baseline, and the derived thresholds are ordered
// high > medium > low and bracketed by the two scores.
func TestRunCalibration_EndToEnd(t *testing.T) {
	storage, err := NewSQLiteStorage(filepath.Join(t.TempDir(), "cal.db"), 4)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer storage.Close()

	ctx := context.Background()
	// Each chunk's content embeds to its own basis vector; the unrelated probe text
	// embeds to a vector that is a poor match for all of them.
	byText := map[string][]float32{
		"alpha content": {1, 0, 0, 0},
		"beta content":  {0, 1, 0, 0},
	}
	seed := []struct {
		chunk Chunk
		emb   []float32
	}{
		{Chunk{ID: "a", FilePath: "a.go", Name: "A", Type: ChunkFunction, Content: "alpha content"}, []float32{1, 0, 0, 0}},
		{Chunk{ID: "b", FilePath: "b.go", Name: "B", Type: ChunkFunction, Content: "beta content"}, []float32{0, 1, 0, 0}},
	}
	for _, s := range seed {
		if err := storage.Create(ctx, s.chunk, s.emb); err != nil {
			t.Fatalf("failed to create chunk %s: %v", s.chunk.ID, err)
		}
	}

	embedder := &tableEmbedder{byText: byText, fallback: []float32{0.1, 0.1, 1, 0}}

	meta, err := RunCalibration(ctx, storage, embedder, "test-model")
	if err != nil {
		t.Fatalf("RunCalibration() error = %v", err)
	}

	if meta.EmbeddingModel != "test-model" {
		t.Errorf("EmbeddingModel = %q, want test-model", meta.EmbeddingModel)
	}
	if meta.PerfectMatchScore < 0.99 {
		t.Errorf("PerfectMatchScore = %v, want ~1.0 (a chunk matches itself)", meta.PerfectMatchScore)
	}
	if meta.BaselineScore >= meta.PerfectMatchScore {
		t.Errorf("BaselineScore %v should be below PerfectMatchScore %v", meta.BaselineScore, meta.PerfectMatchScore)
	}
	if meta.ScoreRange != meta.PerfectMatchScore-meta.BaselineScore {
		t.Errorf("ScoreRange = %v, want PerfectMatchScore - BaselineScore", meta.ScoreRange)
	}
	if !(meta.HighThreshold > meta.MediumThreshold && meta.MediumThreshold > meta.LowThreshold) {
		t.Errorf("thresholds not ordered: high=%v medium=%v low=%v",
			meta.HighThreshold, meta.MediumThreshold, meta.LowThreshold)
	}
	if meta.CalibrationDate.IsZero() {
		t.Error("CalibrationDate should be stamped")
	}
}

// TestRunCalibration_EmptyIndex pins that calibrating an empty index is reported as
// ErrEmptyIndex rather than producing meaningless thresholds.
func TestRunCalibration_EmptyIndexIsReported(t *testing.T) {
	storage, err := NewSQLiteStorage(filepath.Join(t.TempDir(), "empty.db"), 4)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer storage.Close()

	_, err = RunCalibration(context.Background(), storage, &tableEmbedder{fallback: []float32{1, 0, 0, 0}}, "m")
	if !errors.Is(err, ErrEmptyIndex) {
		t.Errorf("RunCalibration() on an empty index = %v, want ErrEmptyIndex", err)
	}
}

// TestRunSelfMatchProbes_Errors pins the two guards on the self-match probe: a
// non-positive probe count, and an index with nothing to probe.
func TestRunSelfMatchProbes_Errors(t *testing.T) {
	storage, err := NewSQLiteStorage(filepath.Join(t.TempDir(), "probes.db"), 4)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer storage.Close()

	embedder := &tableEmbedder{fallback: []float32{1, 0, 0, 0}}
	ctx := context.Background()

	if _, err := runSelfMatchProbes(ctx, storage, embedder, 0); err == nil ||
		!strings.Contains(err.Error(), "probe count must be positive") {
		t.Errorf("runSelfMatchProbes(count=0) error = %v, want a probe-count error", err)
	}

	if _, err := runSelfMatchProbes(ctx, storage, embedder, 3); !errors.Is(err, ErrEmptyIndex) {
		t.Errorf("runSelfMatchProbes on an empty index = %v, want ErrEmptyIndex", err)
	}
}

// TestRunSelfMatchProbes_EmbedderFailure pins that an embedder failure during probing is
// wrapped and returned, not treated as a zero score.
func TestRunSelfMatchProbes_EmbedderFailure(t *testing.T) {
	storage, err := NewSQLiteStorage(filepath.Join(t.TempDir(), "probefail.db"), 4)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer storage.Close()

	ctx := context.Background()
	if err := storage.Create(ctx, Chunk{ID: "a", FilePath: "a.go", Name: "A", Type: ChunkFunction, Content: "x"}, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	_, err = runSelfMatchProbes(ctx, storage, &tableEmbedder{err: errors.New("embed boom")}, 2)
	if err == nil || !strings.Contains(err.Error(), "embed boom") {
		t.Errorf("runSelfMatchProbes() error = %v, want the embedder failure", err)
	}
}

// TestSearcher_UsesStoredCalibrationForLabels pins the calibration branch of relevance
// labelling. The two labellers disagree by construction: the percentile fallback labels
// by RANK, so the best result in a set is always "high", while calibration labels by
// ABSOLUTE score. Storing thresholds no result can reach therefore makes the top result
// "low" - which only happens if the stored calibration is actually consulted.
func TestSearcher_UsesStoredCalibrationForLabels(t *testing.T) {
	storage := prefilterFixture(t)
	ctx := context.Background()
	// Chunk a scores ~1.0 against this query; chunks b and c score 0.
	searcher := NewSearcher(storage, &tableEmbedder{fallback: []float32{1, 0, 0, 0}})

	// Thresholds above the clamped maximum score of 1.0 are unreachable.
	unreachable := &CalibrationMetadata{
		EmbeddingModel:    "test-model",
		PerfectMatchScore: 1.0,
		BaselineScore:     0.0,
		ScoreRange:        1.0,
		HighThreshold:     1.1,
		MediumThreshold:   1.05,
		LowThreshold:      1.01,
	}
	if err := storage.SetCalibrationMetadata(ctx, unreachable); err != nil {
		t.Fatalf("SetCalibrationMetadata() error = %v", err)
	}

	results, err := searcher.Search(ctx, "anything", SearchOptions{TopK: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	if results[0].Chunk.ID != "a" {
		t.Fatalf("top result = %q, want chunk a", results[0].Chunk.ID)
	}
	if results[0].Relevance != "low" {
		t.Errorf("top result relevance = %q, want \"low\": no score can reach the stored thresholds, "+
			"so a \"high\" here means the percentile fallback ran instead of the calibration", results[0].Relevance)
	}
	if results[0].Preview == "" {
		t.Error("calibrated results should still carry previews")
	}

	// With realistic thresholds the same 1.0-scoring chunk is "high" and the
	// zero-scoring ones are not.
	realistic := &CalibrationMetadata{
		EmbeddingModel:    "test-model",
		PerfectMatchScore: 1.0,
		BaselineScore:     0.0,
		ScoreRange:        1.0,
		HighThreshold:     0.8,
		MediumThreshold:   0.5,
		LowThreshold:      0.2,
	}
	if err := storage.SetCalibrationMetadata(ctx, realistic); err != nil {
		t.Fatalf("SetCalibrationMetadata() error = %v", err)
	}
	results, err = searcher.Search(ctx, "anything", SearchOptions{TopK: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	byID := map[string]string{}
	for _, r := range results {
		byID[r.Chunk.ID] = r.Relevance
	}
	if byID["a"] != "high" {
		t.Errorf("chunk a relevance = %q, want \"high\" under the realistic thresholds", byID["a"])
	}
	if byID["b"] != "low" || byID["c"] != "low" {
		t.Errorf("zero-scoring chunks = %q/%q, want both \"low\"", byID["b"], byID["c"])
	}
}
