package semantic

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// memoryFilterFixture stores four memory entries spread across sources, statuses and
// tags, with orthogonal embeddings so cosine scores are exactly 1 or 0.
func memoryFilterFixture(t *testing.T) *SQLiteStorage {
	t.Helper()
	storage, err := NewSQLiteStorage(filepath.Join(t.TempDir(), "memfilter.db"), 4)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	ctx := context.Background()
	entries := []MemoryWithEmbedding{
		{Entry: MemoryEntry{
			ID: "m1", Question: "q1", Answer: "a1", Tags: []string{"go", "sqlite"},
			Source: "sprint", Status: MemoryStatusPending,
			CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
		}, Embedding: []float32{1, 0, 0, 0}},
		{Entry: MemoryEntry{
			ID: "m2", Question: "q2", Answer: "a2", Tags: []string{"rust"},
			Source: "manual", Status: MemoryStatusPromoted,
			CreatedAt: "2026-01-02T00:00:00Z", UpdatedAt: "2026-01-02T00:00:00Z",
		}, Embedding: []float32{1, 0, 0, 0}},
		{Entry: MemoryEntry{
			ID: "m3", Question: "q3", Answer: "a3", Tags: []string{"go"},
			Source: "manual", Status: MemoryStatusPending,
			CreatedAt: "2026-01-03T00:00:00Z", UpdatedAt: "2026-01-03T00:00:00Z",
		}, Embedding: []float32{0, 1, 0, 0}},
		{Entry: MemoryEntry{
			ID: "m4", Question: "q4", Answer: "a4",
			Source: "sprint", Status: MemoryStatusPending,
			Sprints:   []string{"7.0", "7.1"},
			Files:     []string{"a.go", "b.go"},
			CreatedAt: "2026-01-04T00:00:00Z", UpdatedAt: "2026-01-04T00:00:00Z",
		}, Embedding: []float32{1, 0, 0, 0}},
	}
	if err := storage.StoreMemoryBatch(ctx, entries); err != nil {
		t.Fatalf("StoreMemoryBatch() error = %v", err)
	}
	return storage
}

func memoryIDs(results []MemorySearchResult) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.Entry.ID
	}
	return out
}

// TestSearchMemory_ThresholdFilter pins that entries scoring below the threshold are
// dropped before they reach the topK heap: m3's embedding is orthogonal to the query.
func TestSearchMemory_ThresholdFilter(t *testing.T) {
	storage := memoryFilterFixture(t)

	results, err := storage.SearchMemory(context.Background(), []float32{1, 0, 0, 0}, MemorySearchOptions{
		TopK: 10, Threshold: 0.5,
	})
	if err != nil {
		t.Fatalf("SearchMemory() error = %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %v, want the three entries scoring 1.0", memoryIDs(results))
	}
	for _, r := range results {
		if r.Entry.ID == "m3" {
			t.Error("m3 scores 0.0 against the query and must be filtered by the threshold")
		}
		if r.Score < 0.99 {
			t.Errorf("entry %s score = %v, want ~1.0", r.Entry.ID, r.Score)
		}
	}
}

// TestSearchMemory_TagFilter pins the case-insensitive any-of tag filter.
func TestSearchMemory_TagFilter(t *testing.T) {
	storage := memoryFilterFixture(t)

	results, err := storage.SearchMemory(context.Background(), []float32{1, 0, 0, 0}, MemorySearchOptions{
		TopK: 10, Tags: []string{"GO"},
	})
	if err != nil {
		t.Fatalf("SearchMemory() error = %v", err)
	}
	got := memoryIDs(results)
	if len(got) != 2 {
		t.Fatalf("tag filter returned %v, want m1 and m3 (matched case-insensitively)", got)
	}
	found := map[string]bool{}
	for _, id := range got {
		found[id] = true
	}
	if !found["m1"] || !found["m3"] {
		t.Errorf("tag filter returned %v, want m1 and m3", got)
	}
}

// TestSearchMemory_SourceAndStatusFilters pins the exact-match source and status filters.
func TestSearchMemory_SourceAndStatusFilters(t *testing.T) {
	storage := memoryFilterFixture(t)
	ctx := context.Background()

	bySource, err := storage.SearchMemory(ctx, []float32{1, 0, 0, 0}, MemorySearchOptions{
		TopK: 10, Source: "sprint",
	})
	if err != nil {
		t.Fatalf("SearchMemory() error = %v", err)
	}
	if len(bySource) != 2 {
		t.Errorf("source filter returned %v, want m1 and m4", memoryIDs(bySource))
	}

	byStatus, err := storage.SearchMemory(ctx, []float32{1, 0, 0, 0}, MemorySearchOptions{
		TopK: 10, Status: MemoryStatusPromoted,
	})
	if err != nil {
		t.Fatalf("SearchMemory() error = %v", err)
	}
	if len(byStatus) != 1 || byStatus[0].Entry.ID != "m2" {
		t.Errorf("status filter returned %v, want just m2", memoryIDs(byStatus))
	}
}

// TestSearchMemory_TopKHeapKeepsBestScores pins the bounded top-K heap: with TopK=1 only
// the single best-scoring entry survives, and results come back sorted descending.
func TestSearchMemory_TopKHeapKeepsBestScores(t *testing.T) {
	storage := memoryFilterFixture(t)
	ctx := context.Background()

	top, err := storage.SearchMemory(ctx, []float32{0, 1, 0, 0}, MemorySearchOptions{TopK: 1})
	if err != nil {
		t.Fatalf("SearchMemory() error = %v", err)
	}
	if len(top) != 1 {
		t.Fatalf("TopK=1 returned %d results, want 1", len(top))
	}
	if top[0].Entry.ID != "m3" {
		t.Errorf("TopK=1 kept %q, want m3 (the only entry aligned with the query)", top[0].Entry.ID)
	}

	all, err := storage.SearchMemory(ctx, []float32{1, 0, 0, 0}, MemorySearchOptions{TopK: 10})
	if err != nil {
		t.Fatalf("SearchMemory() error = %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("got %d results, want all 4 entries", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].Score > all[i-1].Score {
			t.Errorf("results not sorted descending by score: %v", all)
		}
	}
	// The multi-valued columns must survive the round trip.
	for _, r := range all {
		if r.Entry.ID != "m4" {
			continue
		}
		if len(r.Entry.Sprints) != 2 || r.Entry.Sprints[0] != "7.0" {
			t.Errorf("m4 Sprints = %v, want [7.0 7.1]", r.Entry.Sprints)
		}
		if len(r.Entry.Files) != 2 || r.Entry.Files[1] != "b.go" {
			t.Errorf("m4 Files = %v, want [a.go b.go]", r.Entry.Files)
		}
		if len(r.Embedding) != 4 {
			t.Errorf("m4 Embedding length = %d, want 4", len(r.Embedding))
		}
	}
}

// TestSearchMemory_ClosedStorage pins the closed-handle guard.
func TestSearchMemory_ClosedStorage(t *testing.T) {
	storage, err := NewSQLiteStorage(filepath.Join(t.TempDir(), "closed.db"), 4)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if _, err := storage.SearchMemory(context.Background(), []float32{1, 0, 0, 0}, MemorySearchOptions{}); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("SearchMemory() on closed storage = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.ListMemory(context.Background(), MemoryListOptions{}); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("ListMemory() on closed storage = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.DeleteByDomain(context.Background(), "code"); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("DeleteByDomain() on closed storage = %v, want ErrStorageClosed", err)
	}
}

// TestListMemory_FiltersAndPaging pins ListMemory's ordering (newest first), its SQL
// status/source filters, its LIMIT/OFFSET paging and its in-memory tag filter.
func TestListMemory_FiltersAndPaging(t *testing.T) {
	storage := memoryFilterFixture(t)
	ctx := context.Background()

	all, err := storage.ListMemory(ctx, MemoryListOptions{})
	if err != nil {
		t.Fatalf("ListMemory() error = %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("ListMemory() returned %d entries, want 4", len(all))
	}
	if all[0].ID != "m4" || all[3].ID != "m1" {
		t.Errorf("ListMemory() order = %v, want newest created_at first (m4..m1)", entryIDs(all))
	}

	limited, err := storage.ListMemory(ctx, MemoryListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("ListMemory(limit) error = %v", err)
	}
	if len(limited) != 2 || limited[0].ID != "m4" {
		t.Errorf("ListMemory(limit=2) = %v, want [m4 m3]", entryIDs(limited))
	}

	paged, err := storage.ListMemory(ctx, MemoryListOptions{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("ListMemory(offset) error = %v", err)
	}
	if len(paged) != 2 || paged[0].ID != "m2" || paged[1].ID != "m1" {
		t.Errorf("ListMemory(limit=2, offset=2) = %v, want [m2 m1]", entryIDs(paged))
	}

	byStatus, err := storage.ListMemory(ctx, MemoryListOptions{Status: MemoryStatusPromoted})
	if err != nil {
		t.Fatalf("ListMemory(status) error = %v", err)
	}
	if len(byStatus) != 1 || byStatus[0].ID != "m2" {
		t.Errorf("status filter = %v, want [m2]", entryIDs(byStatus))
	}

	bySource, err := storage.ListMemory(ctx, MemoryListOptions{Source: "sprint"})
	if err != nil {
		t.Fatalf("ListMemory(source) error = %v", err)
	}
	if len(bySource) != 2 {
		t.Errorf("source filter = %v, want m4 and m1", entryIDs(bySource))
	}

	byTag, err := storage.ListMemory(ctx, MemoryListOptions{Tags: []string{"sqlite"}})
	if err != nil {
		t.Fatalf("ListMemory(tags) error = %v", err)
	}
	if len(byTag) != 1 || byTag[0].ID != "m1" {
		t.Errorf("tag filter = %v, want [m1]", entryIDs(byTag))
	}
	if len(byTag[0].Sprints) != 0 {
		t.Errorf("m1 Sprints = %v, want empty", byTag[0].Sprints)
	}

	noMatch, err := storage.ListMemory(ctx, MemoryListOptions{Tags: []string{"cobol"}})
	if err != nil {
		t.Fatalf("ListMemory(tags) error = %v", err)
	}
	if len(noMatch) != 0 {
		t.Errorf("unmatched tag filter = %v, want none", entryIDs(noMatch))
	}
}

// TestDeleteByDomain pins that deletion is scoped to one domain and reports the row
// count, and that chunks stored without a domain fall into the default "code" bucket.
func TestDeleteByDomain(t *testing.T) {
	storage, err := NewSQLiteStorage(filepath.Join(t.TempDir(), "domains.db"), 4)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer storage.Close()

	ctx := context.Background()
	seed := []Chunk{
		{ID: "c1", FilePath: "a.go", Name: "A", Type: ChunkFunction, Content: "a", Domain: "code"},
		{ID: "c2", FilePath: "b.md", Name: "B", Type: ChunkFunction, Content: "b", Domain: "docs"},
		{ID: "c3", FilePath: "c.md", Name: "C", Type: ChunkFunction, Content: "c", Domain: "docs"},
		{ID: "c4", FilePath: "d.go", Name: "D", Type: ChunkFunction, Content: "d"}, // no domain
	}
	for _, c := range seed {
		if err := storage.Create(ctx, c, []float32{1, 0, 0, 0}); err != nil {
			t.Fatalf("failed to create chunk %s: %v", c.ID, err)
		}
	}

	removed, err := storage.DeleteByDomain(ctx, "docs")
	if err != nil {
		t.Fatalf("DeleteByDomain() error = %v", err)
	}
	if removed != 2 {
		t.Errorf("DeleteByDomain(\"docs\") removed %d chunks, want 2", removed)
	}

	remaining, err := storage.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("%d chunks remain, want 2", len(remaining))
	}
	for _, c := range remaining {
		if c.Domain == "docs" {
			t.Errorf("chunk %s in the docs domain survived the delete", c.ID)
		}
	}

	// The domainless chunk is bucketed as "code" by the COALESCE in the delete.
	removed, err = storage.DeleteByDomain(ctx, "code")
	if err != nil {
		t.Fatalf("DeleteByDomain() error = %v", err)
	}
	if removed != 2 {
		t.Errorf("DeleteByDomain(\"code\") removed %d chunks, want 2 (including the domainless one)", removed)
	}

	none, err := storage.DeleteByDomain(ctx, "nothing-here")
	if err != nil {
		t.Fatalf("DeleteByDomain() error = %v", err)
	}
	if none != 0 {
		t.Errorf("DeleteByDomain on an unused domain removed %d chunks, want 0", none)
	}
}

// TestSortMemoryResultsByScore pins the descending in-place sort used on memory results.
func TestSortMemoryResultsByScore(t *testing.T) {
	results := []MemorySearchResult{
		{Entry: MemoryEntry{ID: "low"}, Score: 0.1},
		{Entry: MemoryEntry{ID: "high"}, Score: 0.9},
		{Entry: MemoryEntry{ID: "mid"}, Score: 0.5},
	}

	sortMemoryResultsByScore(results)

	want := []string{"high", "mid", "low"}
	got := memoryIDs(results)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortMemoryResultsByScore() = %v, want %v", got, want)
		}
	}

	// A single element and an empty slice must not panic on the len-1 loop bound.
	sortMemoryResultsByScore(results[:1])
	sortMemoryResultsByScore(nil)
}

// TestIndexPath pins the on-disk layout the CLI depends on: <repo>/.index/semantic.db.
func TestIndexPath(t *testing.T) {
	got := IndexPath("/tmp/repo")
	want := filepath.Join("/tmp/repo", ".index", "semantic.db")
	if got != want {
		t.Errorf("IndexPath() = %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, filepath.Join(".index", "semantic.db")) {
		t.Errorf("IndexPath() = %q, want it to end with .index/semantic.db", got)
	}
}

func entryIDs(entries []MemoryEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return out
}
