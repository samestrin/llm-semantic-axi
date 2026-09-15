package semantic

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestQdrantStorage_AgainstFakeServer runs the shared Storage contract against
// QdrantStorage talking to the in-process Qdrant. It pins that the Qdrant
// backend honours the same CRUD, list, search, stats and file-hash behaviour
// every Storage implementation must.
func TestQdrantStorage_AgainstFakeServer(t *testing.T) {
	StorageTestSuite(t, func() (Storage, func()) {
		storage, _ := newFakeQdrantStorage(t, "contract", 4)
		return storage, func() { _ = storage.Close() }
	})
}

// TestQdrantStorageMemory_AgainstFakeServer runs the shared memory-storage
// contract against the Qdrant backend.
func TestQdrantStorageMemory_AgainstFakeServer(t *testing.T) {
	MemoryStorageTestSuite(t, func() (Storage, func()) {
		storage, _ := newFakeQdrantStorage(t, "memory_contract", 4)
		return storage, func() { _ = storage.Close() }
	})
}

// TestQdrantStorage_EnsureCollectionCreatesOnce pins that construction creates
// the collection with the configured dimension, and that pointing a second
// storage at the same collection reuses it instead of re-creating it.
func TestQdrantStorage_EnsureCollectionCreatesOnce(t *testing.T) {
	fq, url := startFakeQdrant(t)

	cfg := QdrantConfig{
		APIKey:         "test-key",
		URL:            url,
		CollectionName: "ensure",
		EmbeddingDim:   8,
		FTSDataDir:     t.TempDir(),
	}

	first, err := NewQdrantStorage(cfg)
	if err != nil {
		t.Fatalf("NewQdrantStorage: %v", err)
	}
	defer first.Close()

	if !fq.hasCollection("ensure") {
		t.Fatal("expected the collection to be created on construction")
	}
	if got := fq.collectionDim("ensure"); got != 8 {
		t.Errorf("collection vector size = %d, want 8", got)
	}
	if n := fq.callCount("CreateCollection"); n != 1 {
		t.Errorf("CreateCollection called %d times, want 1", n)
	}

	cfg.FTSDataDir = t.TempDir()
	second, err := NewQdrantStorage(cfg)
	if err != nil {
		t.Fatalf("NewQdrantStorage (second): %v", err)
	}
	defer second.Close()

	if n := fq.callCount("CreateCollection"); n != 1 {
		t.Errorf("CreateCollection called %d times after reuse, want 1", n)
	}
}

// TestQdrantStorage_NewWrapsEnsureCollectionFailure pins that a collection
// listing failure aborts construction with a wrapped error rather than
// returning a half-built storage.
func TestQdrantStorage_NewWrapsEnsureCollectionFailure(t *testing.T) {
	fq, url := startFakeQdrant(t)
	fq.setFailure("ListCollections", status.Error(codes.Unavailable, "cluster down"))

	storage, err := NewQdrantStorage(QdrantConfig{
		APIKey:         "test-key",
		URL:            url,
		CollectionName: "broken",
		EmbeddingDim:   4,
		FTSDataDir:     t.TempDir(),
	})
	if err == nil {
		storage.Close()
		t.Fatal("expected construction to fail when the collection cannot be listed")
	}
	if !strings.Contains(err.Error(), "failed to ensure collection") {
		t.Errorf("error = %q, want it to mention ensure collection", err)
	}
	if !strings.Contains(err.Error(), "failed to list collections") {
		t.Errorf("error = %q, want the underlying list failure preserved", err)
	}
}

// TestQdrantStorage_NewWrapsCreateCollectionFailure pins the other half of
// ensureCollection: a create failure is surfaced, not swallowed.
func TestQdrantStorage_NewWrapsCreateCollectionFailure(t *testing.T) {
	fq, url := startFakeQdrant(t)
	fq.setFailure("CreateCollection", status.Error(codes.PermissionDenied, "read-only"))

	_, err := NewQdrantStorage(QdrantConfig{
		APIKey:         "test-key",
		URL:            url,
		CollectionName: "nocreate",
		EmbeddingDim:   4,
		FTSDataDir:     t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected construction to fail when the collection cannot be created")
	}
	if !strings.Contains(err.Error(), "failed to create collection") {
		t.Errorf("error = %q, want it to mention create collection", err)
	}
}

// TestQdrantStorage_RequiresFTSPath pins that a non-in-memory storage with no
// FTS data directory is rejected instead of silently indexing nowhere.
func TestQdrantStorage_RequiresFTSPath(t *testing.T) {
	_, url := startFakeQdrant(t)

	_, err := NewQdrantStorage(QdrantConfig{
		APIKey:         "test-key",
		URL:            url,
		CollectionName: "nofts",
		EmbeddingDim:   4,
	})
	if err == nil {
		t.Fatal("expected an error when FTSDataDir is empty and FTS is not in-memory")
	}
	if !strings.Contains(err.Error(), "FTSDataDir is required") {
		t.Errorf("error = %q, want it to name FTSDataDir", err)
	}
}

// TestQdrantStorage_InMemoryFTS pins that the in-memory FTS option produces a
// working lexical index without touching disk.
func TestQdrantStorage_InMemoryFTS(t *testing.T) {
	_, url := startFakeQdrant(t)

	storage, err := NewQdrantStorage(QdrantConfig{
		APIKey:         "test-key",
		URL:            url,
		CollectionName: "inmem",
		EmbeddingDim:   4,
		InMemoryFTS:    true,
	})
	if err != nil {
		t.Fatalf("NewQdrantStorage: %v", err)
	}
	defer storage.Close()

	chunk := Chunk{ID: "mem-1", FilePath: "/a.go", Type: ChunkFunction, Name: "Alpha", Content: "func Alpha() { retryBudget() }", Language: "go"}
	if err := storage.Create(context.Background(), chunk, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	results, err := storage.LexicalSearch(context.Background(), "retryBudget", LexicalSearchOptions{TopK: 5})
	if err != nil {
		t.Fatalf("LexicalSearch: %v", err)
	}
	if len(results) != 1 || results[0].Chunk.ID != "mem-1" {
		t.Fatalf("LexicalSearch returned %d results, want the one indexed chunk", len(results))
	}
}

// TestQdrantStorage_CreateBatchSplitsLargeBatches pins that a batch above
// MaxQdrantBatchSize is sent as several Upsert requests, and that every chunk
// still lands.
func TestQdrantStorage_CreateBatchSplitsLargeBatches(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "batching", 4)

	const n = MaxQdrantBatchSize*2 + 5
	batch := make([]ChunkWithEmbedding, 0, n)
	for i := 0; i < n; i++ {
		batch = append(batch, ChunkWithEmbedding{
			Chunk: Chunk{
				ID:       fmt.Sprintf("big-%03d", i),
				FilePath: "/big.go",
				Type:     ChunkFunction,
				Name:     fmt.Sprintf("Fn%03d", i),
				Language: "go",
			},
			Embedding: []float32{1, 0, 0, 0},
		})
	}

	before := fq.callCount("Upsert")
	if err := storage.CreateBatch(context.Background(), batch); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}

	wantCalls := (n + MaxQdrantBatchSize - 1) / MaxQdrantBatchSize
	if got := fq.callCount("Upsert") - before; got != wantCalls {
		t.Errorf("Upsert called %d times, want %d sub-batches", got, wantCalls)
	}
	if got := fq.pointCount("batching"); got != n {
		t.Errorf("stored %d points, want %d", got, n)
	}
}

// TestQdrantStorage_CreateBatchEmptyIsNoOp pins that an empty batch never
// reaches the server.
func TestQdrantStorage_CreateBatchEmptyIsNoOp(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "emptybatch", 4)

	before := fq.callCount("Upsert")
	if err := storage.CreateBatch(context.Background(), nil); err != nil {
		t.Fatalf("CreateBatch(nil) = %v, want nil", err)
	}
	if got := fq.callCount("Upsert"); got != before {
		t.Error("CreateBatch(nil) issued an Upsert request")
	}
}

// TestQdrantStorage_CreateBatchHonoursCancelledContext pins that a cancelled
// context stops a split batch part-way instead of running to completion.
func TestQdrantStorage_CreateBatchHonoursCancelledContext(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "cancelbatch", 4)

	batch := make([]ChunkWithEmbedding, 0, MaxQdrantBatchSize+1)
	for i := 0; i <= MaxQdrantBatchSize; i++ {
		batch = append(batch, ChunkWithEmbedding{
			Chunk:     Chunk{ID: fmt.Sprintf("c-%03d", i), FilePath: "/c.go", Type: ChunkFunction, Language: "go"},
			Embedding: []float32{1, 0, 0, 0},
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := storage.CreateBatch(ctx, batch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateBatch with a cancelled context = %v, want context.Canceled", err)
	}
}

// TestQdrantStorage_CreateBatchWrapsSubBatchFailure pins that a failed
// sub-batch reports which slice of the batch failed.
func TestQdrantStorage_CreateBatchWrapsSubBatchFailure(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "batchfail", 4)

	batch := make([]ChunkWithEmbedding, 0, MaxQdrantBatchSize+1)
	for i := 0; i <= MaxQdrantBatchSize; i++ {
		batch = append(batch, ChunkWithEmbedding{
			Chunk:     Chunk{ID: fmt.Sprintf("f-%03d", i), FilePath: "/f.go", Type: ChunkFunction, Language: "go"},
			Embedding: []float32{1, 0, 0, 0},
		})
	}

	fq.setFailure("Upsert", status.Error(codes.ResourceExhausted, "too big"))
	err := storage.CreateBatch(context.Background(), batch)
	if err == nil {
		t.Fatal("expected CreateBatch to fail when Upsert fails")
	}
	if !strings.Contains(err.Error(), "sub-batch 0-100") {
		t.Errorf("error = %q, want it to name the failing sub-batch range", err)
	}
}

// TestQdrantStorage_DeleteNotFound pins that deleting an absent chunk reports
// ErrNotFound rather than silently succeeding.
func TestQdrantStorage_DeleteNotFound(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "delmissing", 4)

	err := storage.Delete(context.Background(), "never-stored")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete of a missing chunk = %v, want ErrNotFound", err)
	}
}

// TestQdrantStorage_DeleteRemovesFromLexicalIndex pins that deleting a chunk
// also drops it from the parallel FTS index, so lexical search cannot serve a
// deleted chunk.
func TestQdrantStorage_DeleteRemovesFromLexicalIndex(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "delfts", 4)
	ctx := context.Background()

	chunk := Chunk{ID: "fts-del", FilePath: "/x.go", Type: ChunkFunction, Name: "Zebra", Content: "func Zebra() { quokkaToken() }", Language: "go"}
	if err := storage.Create(ctx, chunk, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	before, err := storage.LexicalSearch(ctx, "quokkaToken", LexicalSearchOptions{TopK: 5})
	if err != nil {
		t.Fatalf("LexicalSearch: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("LexicalSearch before delete returned %d results, want 1", len(before))
	}

	if err := storage.Delete(ctx, "fts-del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	after, err := storage.LexicalSearch(ctx, "quokkaToken", LexicalSearchOptions{TopK: 5})
	if err != nil {
		t.Fatalf("LexicalSearch after delete: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("LexicalSearch after delete returned %d results, want 0", len(after))
	}
}

// TestQdrantStorage_DeleteByDomain pins that domain deletion removes only the
// matching domain and reports how many points went.
func TestQdrantStorage_DeleteByDomain(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "bydomain", 4)
	ctx := context.Background()

	for _, c := range []Chunk{
		{ID: "d1", FilePath: "/a.md", Type: ChunkFunction, Domain: "docs", Language: "md"},
		{ID: "d2", FilePath: "/b.md", Type: ChunkFunction, Domain: "docs", Language: "md"},
		{ID: "d3", FilePath: "/c.go", Type: ChunkFunction, Domain: "code", Language: "go"},
	} {
		if err := storage.Create(ctx, c, []float32{1, 0, 0, 0}); err != nil {
			t.Fatalf("Create %s: %v", c.ID, err)
		}
	}

	n, err := storage.DeleteByDomain(ctx, "docs")
	if err != nil {
		t.Fatalf("DeleteByDomain: %v", err)
	}
	if n != 2 {
		t.Errorf("DeleteByDomain removed %d, want 2", n)
	}

	remaining, err := storage.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != "d3" {
		t.Errorf("remaining chunks = %v, want only d3", remaining)
	}
}

// TestQdrantStorage_DeleteByDomainNoMatch pins that a domain with no points
// short-circuits: it returns zero and issues no delete.
func TestQdrantStorage_DeleteByDomainNoMatch(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "bydomain_none", 4)

	before := fq.callCount("DeletePoints")
	n, err := storage.DeleteByDomain(context.Background(), "nothing-here")
	if err != nil {
		t.Fatalf("DeleteByDomain: %v", err)
	}
	if n != 0 {
		t.Errorf("DeleteByDomain = %d, want 0", n)
	}
	if got := fq.callCount("DeletePoints"); got != before {
		t.Error("DeleteByDomain issued a delete for a domain with no points")
	}
}

// TestQdrantStorage_DeleteByFilePathNoMatch pins the same short-circuit for
// file-path deletion.
func TestQdrantStorage_DeleteByFilePathNoMatch(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "bypath_none", 4)

	before := fq.callCount("DeletePoints")
	n, err := storage.DeleteByFilePath(context.Background(), "/absent.go")
	if err != nil {
		t.Fatalf("DeleteByFilePath: %v", err)
	}
	if n != 0 {
		t.Errorf("DeleteByFilePath = %d, want 0", n)
	}
	if got := fq.callCount("DeletePoints"); got != before {
		t.Error("DeleteByFilePath issued a delete for a path with no points")
	}
}

// TestQdrantStorage_ListPaginatesScroll pins that List keeps scrolling past
// the 256-point page size instead of returning only the first page.
func TestQdrantStorage_ListPaginatesScroll(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "paging", 4)
	ctx := context.Background()

	const n = 600
	batch := make([]ChunkWithEmbedding, 0, n)
	for i := 0; i < n; i++ {
		batch = append(batch, ChunkWithEmbedding{
			Chunk: Chunk{
				ID:       fmt.Sprintf("p-%04d", i),
				FilePath: "/paged.go",
				Type:     ChunkFunction,
				Language: "go",
			},
			Embedding: []float32{1, 0, 0, 0},
		})
	}
	if err := storage.CreateBatch(ctx, batch); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}

	before := fq.callCount("Scroll")
	chunks, err := storage.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(chunks) != n {
		t.Fatalf("List returned %d chunks, want %d", len(chunks), n)
	}
	if got := fq.callCount("Scroll") - before; got < 3 {
		t.Errorf("List issued %d Scroll calls, want at least 3 pages", got)
	}
}

// TestQdrantStorage_ListOffsetAndLimit pins the in-memory offset/limit window
// applied after scrolling.
func TestQdrantStorage_ListOffsetAndLimit(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "listwindow", 4)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		c := Chunk{ID: fmt.Sprintf("w-%d", i), FilePath: "/w.go", Type: ChunkFunction, Name: fmt.Sprintf("W%d", i), Language: "go"}
		if err := storage.Create(ctx, c, []float32{1, 0, 0, 0}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	chunks, err := storage.List(ctx, ListOptions{Offset: 1, Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("List returned %d chunks, want 2", len(chunks))
	}
	if chunks[0].ID != "w-1" || chunks[1].ID != "w-2" {
		t.Errorf("List window = %s,%s, want w-1,w-2", chunks[0].ID, chunks[1].ID)
	}

	// An offset past the end yields nothing rather than panicking.
	empty, err := storage.List(ctx, ListOptions{Offset: 99, Limit: 2})
	if err != nil {
		t.Fatalf("List past the end: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("List past the end returned %d chunks, want 0", len(empty))
	}
}

// TestQdrantStorage_ListFiltersByTypeAndLanguage pins that the type and
// language list options become server-side filter conditions.
func TestQdrantStorage_ListFiltersByTypeAndLanguage(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "listfilter", 4)
	ctx := context.Background()

	for _, c := range []Chunk{
		{ID: "lf1", FilePath: "/a.go", Type: ChunkFunction, Name: "Fn", Language: "go"},
		{ID: "lf2", FilePath: "/a.go", Type: ChunkStruct, Name: "St", Language: "go"},
		{ID: "lf3", FilePath: "/a.py", Type: ChunkFunction, Name: "Py", Language: "python"},
	} {
		if err := storage.Create(ctx, c, []float32{1, 0, 0, 0}); err != nil {
			t.Fatalf("Create %s: %v", c.ID, err)
		}
	}

	byType, err := storage.List(ctx, ListOptions{Type: ChunkFunction.String()})
	if err != nil {
		t.Fatalf("List by type: %v", err)
	}
	if len(byType) != 2 {
		t.Errorf("List by type returned %d, want 2", len(byType))
	}

	byLang, err := storage.List(ctx, ListOptions{Language: "python"})
	if err != nil {
		t.Fatalf("List by language: %v", err)
	}
	if len(byLang) != 1 || byLang[0].ID != "lf3" {
		t.Errorf("List by language = %v, want only lf3", byLang)
	}
}

// TestQdrantStorage_SearchFiltersByPathPrefix pins that PathFilter reaches the
// server as a text match on file_path.
func TestQdrantStorage_SearchFiltersByPathPrefix(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "searchpath", 4)
	ctx := context.Background()

	for _, c := range []struct {
		chunk Chunk
		vec   []float32
	}{
		{Chunk{ID: "sp1", FilePath: "/internal/auth/login.go", Type: ChunkFunction, Name: "Login", Language: "go"}, []float32{1, 0, 0, 0}},
		{Chunk{ID: "sp2", FilePath: "/cmd/main.go", Type: ChunkFunction, Name: "Main", Language: "go"}, []float32{1, 0, 0, 0}},
	} {
		if err := storage.Create(ctx, c.chunk, c.vec); err != nil {
			t.Fatalf("Create %s: %v", c.chunk.ID, err)
		}
	}

	results, err := storage.Search(ctx, []float32{1, 0, 0, 0}, SearchOptions{TopK: 10, PathFilter: "/internal/auth"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Chunk.ID != "sp1" {
		t.Fatalf("Search with a path filter returned %d results, want only sp1", len(results))
	}
}

// TestQdrantStorage_SearchFiltersByChunkIDs pins that the prefilter path turns
// ChunkIDs into a has_id condition, so only the named points can come back.
func TestQdrantStorage_SearchFiltersByChunkIDs(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "searchids", 4)
	ctx := context.Background()

	ids := []string{"ci1", "ci2", "ci3"}
	for _, id := range ids {
		c := Chunk{ID: id, FilePath: "/a.go", Type: ChunkFunction, Name: id, Language: "go"}
		if err := storage.Create(ctx, c, []float32{1, 0, 0, 0}); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}

	// Qdrant point ids are the UUID form of the chunk id.
	wanted := []string{stringToUUID("ci1"), stringToUUID("ci3")}
	results, err := storage.Search(ctx, []float32{1, 0, 0, 0}, SearchOptions{TopK: 10, ChunkIDs: wanted})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Search restricted to 2 ids returned %d results", len(results))
	}
	got := map[string]bool{results[0].Chunk.ID: true, results[1].Chunk.ID: true}
	if !got["ci1"] || !got["ci3"] {
		t.Errorf("Search returned %v, want ci1 and ci3", got)
	}
}

// TestQdrantStorage_SearchDefaultsToTenResults pins the default TopK sent to
// the server when the caller asks for none.
func TestQdrantStorage_SearchDefaultsToTenResults(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "searchdefault", 4)

	if _, err := storage.Search(context.Background(), []float32{1, 0, 0, 0}, SearchOptions{}); err != nil {
		t.Fatalf("Search: %v", err)
	}

	fq.mu.Lock()
	limit := fq.lastQueryLimit
	fq.mu.Unlock()
	if limit != 10 {
		t.Errorf("Search sent limit %d, want the default 10", limit)
	}
}

// TestQdrantStorage_SearchWrapsServerFailure pins the error wrapping on the
// query path.
func TestQdrantStorage_SearchWrapsServerFailure(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "searchfail", 4)
	fq.setFailure("Query", status.Error(codes.Internal, "index corrupt"))

	_, err := storage.Search(context.Background(), []float32{1, 0, 0, 0}, SearchOptions{TopK: 3})
	if err == nil {
		t.Fatal("expected Search to fail when the server errors")
	}
	if !strings.Contains(err.Error(), "failed to search") {
		t.Errorf("error = %q, want it to mention the search failure", err)
	}
}

// TestQdrantStorage_StatsSurvivesScrollFailure pins that the file count is
// best-effort: a failed scroll still yields the point total rather than an
// error.
func TestQdrantStorage_StatsSurvivesScrollFailure(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "statsdegraded", 4)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		c := Chunk{ID: fmt.Sprintf("st-%d", i), FilePath: fmt.Sprintf("/s%d.go", i), Type: ChunkFunction, Language: "go"}
		if err := storage.Create(ctx, c, []float32{1, 0, 0, 0}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	fq.setFailure("Scroll", status.Error(codes.DeadlineExceeded, "slow"))
	stats, err := storage.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats should tolerate a scroll failure, got %v", err)
	}
	if stats.ChunksTotal != 2 {
		t.Errorf("Stats ChunksTotal = %d, want 2", stats.ChunksTotal)
	}
	if stats.FilesIndexed != 0 {
		t.Errorf("Stats FilesIndexed = %d, want 0 when the scroll failed", stats.FilesIndexed)
	}
	if stats.LastUpdated == "" {
		t.Error("Stats LastUpdated is empty")
	}
}

// TestQdrantStorage_StatsWrapsCollectionInfoFailure pins that a failed
// collection lookup is a hard error, unlike the best-effort scroll.
func TestQdrantStorage_StatsWrapsCollectionInfoFailure(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "statsfail", 4)
	fq.setFailure("GetCollectionInfo", status.Error(codes.Unavailable, "no node"))

	_, err := storage.Stats(context.Background())
	if err == nil {
		t.Fatal("expected Stats to fail when the collection info call fails")
	}
	if !strings.Contains(err.Error(), "failed to get collection info") {
		t.Errorf("error = %q, want it to mention collection info", err)
	}
}

// TestQdrantStorage_ClearPreservesCalibrationMetadata pins the documented
// promise of Clear: the collection is dropped and rebuilt, but calibration
// survives.
func TestQdrantStorage_ClearPreservesCalibrationMetadata(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "clearcal", 4)
	ctx := context.Background()

	meta := &CalibrationMetadata{
		EmbeddingModel:    "test-model",
		CalibrationDate:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		PerfectMatchScore: 0.95,
		BaselineScore:     0.2,
		ScoreRange:        0.75,
		HighThreshold:     0.8,
		MediumThreshold:   0.6,
		LowThreshold:      0.4,
	}
	if err := storage.SetCalibrationMetadata(ctx, meta); err != nil {
		t.Fatalf("SetCalibrationMetadata: %v", err)
	}

	chunk := Chunk{ID: "cc-1", FilePath: "/a.go", Type: ChunkFunction, Language: "go"}
	if err := storage.Create(ctx, chunk, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := storage.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	if _, err := storage.Read(ctx, "cc-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Read after Clear = %v, want ErrNotFound", err)
	}

	got, err := storage.GetCalibrationMetadata(ctx)
	if err != nil {
		t.Fatalf("GetCalibrationMetadata: %v", err)
	}
	if got == nil {
		t.Fatal("Clear dropped the calibration metadata")
	}
	if got.EmbeddingModel != meta.EmbeddingModel || got.HighThreshold != meta.HighThreshold || got.BaselineScore != meta.BaselineScore {
		t.Errorf("restored calibration = %+v, want %+v", got, meta)
	}
	if !got.CalibrationDate.Equal(meta.CalibrationDate) {
		t.Errorf("restored calibration date = %v, want %v", got.CalibrationDate, meta.CalibrationDate)
	}
}

// TestQdrantStorage_ClearWrapsDeleteCollectionFailure pins that Clear reports
// a failed collection drop rather than proceeding to recreate.
func TestQdrantStorage_ClearWrapsDeleteCollectionFailure(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "clearfail", 4)
	fq.setFailure("DeleteCollection", status.Error(codes.Unavailable, "busy"))

	err := storage.Clear(context.Background())
	if err == nil {
		t.Fatal("expected Clear to fail when the collection cannot be deleted")
	}
	if !strings.Contains(err.Error(), "failed to delete collection") {
		t.Errorf("error = %q, want it to mention deleting the collection", err)
	}
}

// TestQdrantStorage_GetCalibrationMetadataAbsent pins that an uncalibrated
// index reports (nil, nil) instead of an error.
func TestQdrantStorage_GetCalibrationMetadataAbsent(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "nocal", 4)

	meta, err := storage.GetCalibrationMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetCalibrationMetadata: %v", err)
	}
	if meta != nil {
		t.Errorf("GetCalibrationMetadata = %+v, want nil before any calibration", meta)
	}
}

// TestQdrantStorage_GetCalibrationMetadataRejectsCorruptJSON pins that garbage
// in the calibration payload is reported, not silently ignored.
func TestQdrantStorage_GetCalibrationMetadataRejectsCorruptJSON(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "badcal", 4)
	ctx := context.Background()

	if err := storage.SetCalibrationMetadata(ctx, &CalibrationMetadata{EmbeddingModel: "m"}); err != nil {
		t.Fatalf("SetCalibrationMetadata: %v", err)
	}

	// Corrupt the stored JSON behind the storage's back.
	fq.mu.Lock()
	fq.collections["badcal"].points[calibrationMetadataID()].payload["calibration_json"] = qdrant.NewValueString("{not json")
	fq.mu.Unlock()

	_, err := storage.GetCalibrationMetadata(ctx)
	if err == nil {
		t.Fatal("expected corrupt calibration JSON to be reported")
	}
	if !strings.Contains(err.Error(), "failed to parse calibration metadata") {
		t.Errorf("error = %q, want a parse failure", err)
	}
}

// TestQdrantStorage_GetChunksByFilePath pins the lightweight summary
// projection used by incremental re-index.
func TestQdrantStorage_GetChunksByFilePath(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "summaries", 4)
	ctx := context.Background()

	chunks := []Chunk{
		{ID: "sum-1", FilePath: "/sum.go", Type: ChunkFunction, Name: "One", StartLine: 10, ContentHash: "h1", Language: "go"},
		{ID: "sum-2", FilePath: "/sum.go", Type: ChunkFunction, Name: "Two", StartLine: 40, ContentHash: "h2", Language: "go"},
		{ID: "sum-3", FilePath: "/other.go", Type: ChunkFunction, Name: "Three", StartLine: 1, ContentHash: "h3", Language: "go"},
	}
	for _, c := range chunks {
		if err := storage.Create(ctx, c, []float32{1, 0, 0, 0}); err != nil {
			t.Fatalf("Create %s: %v", c.ID, err)
		}
	}

	summaries, err := storage.GetChunksByFilePath(ctx, "/sum.go")
	if err != nil {
		t.Fatalf("GetChunksByFilePath: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("GetChunksByFilePath returned %d summaries, want 2", len(summaries))
	}

	byHash := map[string]ChunkSummary{}
	for _, s := range summaries {
		byHash[s.ContentHash] = s
	}
	one, ok := byHash["h1"]
	if !ok {
		t.Fatalf("summaries missing content hash h1: %+v", summaries)
	}
	if one.Name != "One" || one.StartLine != 10 {
		t.Errorf("summary for h1 = %+v, want Name=One StartLine=10", one)
	}
	if one.ID != stringToUUID("sum-1") {
		t.Errorf("summary ID = %q, want the point UUID", one.ID)
	}
}

// TestQdrantStorage_GetChunksByFilePathWrapsFailure pins the error wrapping on
// the summary scroll.
func TestQdrantStorage_GetChunksByFilePathWrapsFailure(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "summariesfail", 4)
	fq.setFailure("Scroll", status.Error(codes.Internal, "boom"))

	_, err := storage.GetChunksByFilePath(context.Background(), "/x.go")
	if err == nil {
		t.Fatal("expected GetChunksByFilePath to fail when the scroll fails")
	}
	if !strings.Contains(err.Error(), "failed to scroll chunks by file path") {
		t.Errorf("error = %q, want it to name the scroll", err)
	}
}

// TestQdrantStorage_ReadEmbeddings pins that stored vectors round-trip and
// that unknown ids are simply absent from the result.
func TestQdrantStorage_ReadEmbeddings(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "embeddings", 4)
	ctx := context.Background()

	want := []float32{0.25, 0.5, 0.75, 1}
	chunk := Chunk{ID: "emb-1", FilePath: "/e.go", Type: ChunkFunction, Language: "go"}
	if err := storage.Create(ctx, chunk, want); err != nil {
		t.Fatalf("Create: %v", err)
	}

	id := stringToUUID("emb-1")
	got, err := storage.ReadEmbeddings(ctx, []string{id, stringToUUID("missing")})
	if err != nil {
		t.Fatalf("ReadEmbeddings: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ReadEmbeddings returned %d vectors, want 1", len(got))
	}
	vec := got[id]
	if len(vec) != len(want) {
		t.Fatalf("vector length = %d, want %d", len(vec), len(want))
	}
	for i := range want {
		if vec[i] != want[i] {
			t.Errorf("vector[%d] = %v, want %v", i, vec[i], want[i])
		}
	}
}

// TestQdrantStorage_ReadEmbeddingsEmptyInput pins the no-request short-circuit
// for an empty id list.
func TestQdrantStorage_ReadEmbeddingsEmptyInput(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "embeddings_empty", 4)

	before := fq.callCount("GetPoints")
	got, err := storage.ReadEmbeddings(context.Background(), nil)
	if err != nil {
		t.Fatalf("ReadEmbeddings(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ReadEmbeddings(nil) = %v, want an empty map", got)
	}
	if fq.callCount("GetPoints") != before {
		t.Error("ReadEmbeddings(nil) issued a Get request")
	}
}

// TestQdrantStorage_ClosedStorageRejectsEveryOperation pins that the closed
// flag is checked on every entry point rather than a handful of them.
func TestQdrantStorage_ClosedStorageRejectsEveryOperation(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "closed", 4)
	ctx := context.Background()

	if err := storage.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close is idempotent.
	if err := storage.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	checks := []struct {
		name string
		err  error
	}{
		{"Create", storage.Create(ctx, Chunk{ID: "x"}, []float32{1, 0, 0, 0})},
		{"CreateBatch", storage.CreateBatch(ctx, []ChunkWithEmbedding{{Chunk: Chunk{ID: "x"}, Embedding: []float32{1, 0, 0, 0}}})},
		{"Delete", storage.Delete(ctx, "x")},
		{"SetFileHash", storage.SetFileHash(ctx, "/a.go", "h")},
		{"StoreMemory", storage.StoreMemory(ctx, MemoryEntry{ID: "m"}, []float32{1, 0, 0, 0})},
		{"StoreMemoryBatch", storage.StoreMemoryBatch(ctx, []MemoryWithEmbedding{{Entry: MemoryEntry{ID: "m"}, Embedding: []float32{1, 0, 0, 0}}})},
		{"DeleteMemory", storage.DeleteMemory(ctx, "m")},
		{"SetCalibrationMetadata", storage.SetCalibrationMetadata(ctx, &CalibrationMetadata{})},
		{"TrackMemoryRetrieval", storage.TrackMemoryRetrieval(ctx, "m", "q", 1)},
		{"UpdateMemoryStatsStatus", storage.UpdateMemoryStatsStatus(ctx, "m", "active")},
	}
	for _, c := range checks {
		if !errors.Is(c.err, ErrStorageClosed) {
			t.Errorf("%s on a closed storage = %v, want ErrStorageClosed", c.name, c.err)
		}
	}

	if _, err := storage.Read(ctx, "x"); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("Read = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.List(ctx, ListOptions{}); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("List = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.Search(ctx, []float32{1, 0, 0, 0}, SearchOptions{}); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("Search = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.Stats(ctx); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("Stats = %v, want ErrStorageClosed", err)
	}
	if err := storage.Clear(ctx); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("Clear = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.DeleteByFilePath(ctx, "/a.go"); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("DeleteByFilePath = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.DeleteByDomain(ctx, "code"); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("DeleteByDomain = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.GetChunksByFilePath(ctx, "/a.go"); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("GetChunksByFilePath = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.ReadEmbeddings(ctx, []string{"x"}); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("ReadEmbeddings = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.LexicalSearch(ctx, "q", LexicalSearchOptions{}); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("LexicalSearch = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.ListIndexedFiles(ctx); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("ListIndexedFiles = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.GetFileHash(ctx, "/a.go"); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("GetFileHash = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.GetMemory(ctx, "m"); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("GetMemory = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.ListMemory(ctx, MemoryListOptions{}); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("ListMemory = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.SearchMemory(ctx, []float32{1, 0, 0, 0}, MemorySearchOptions{}); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("SearchMemory = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.GetCalibrationMetadata(ctx); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("GetCalibrationMetadata = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.GetMemoryStats(ctx, "m"); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("GetMemoryStats = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.GetAllMemoryStats(ctx); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("GetAllMemoryStats = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.GetMemoryRetrievalHistory(ctx, "m", 10); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("GetMemoryRetrievalHistory = %v, want ErrStorageClosed", err)
	}
	if _, err := storage.PruneMemoryRetrievalLog(ctx, 30); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("PruneMemoryRetrievalLog = %v, want ErrStorageClosed", err)
	}
}

// TestQdrantStorage_DeleteCollection pins that DeleteCollection really drops
// the collection on the server.
func TestQdrantStorage_DeleteCollection(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "dropme", 4)

	if !fq.hasCollection("dropme") {
		t.Fatal("collection missing before the drop")
	}
	if err := storage.DeleteCollection(); err != nil {
		t.Fatalf("DeleteCollection: %v", err)
	}
	if fq.hasCollection("dropme") {
		t.Error("DeleteCollection left the collection in place")
	}
}

// TestQdrantStorage_MemoryTagFiltering pins the post-search tag filter, which
// Qdrant cannot express server-side, including its case-insensitivity.
func TestQdrantStorage_MemoryTagFiltering(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "memtags", 4)
	ctx := context.Background()

	entries := []MemoryWithEmbedding{
		{Entry: MemoryEntry{ID: "m1", Question: "q1", Answer: "a1", Tags: []string{"Go", "storage"}, Status: MemoryStatusPending}, Embedding: []float32{1, 0, 0, 0}},
		{Entry: MemoryEntry{ID: "m2", Question: "q2", Answer: "a2", Tags: []string{"python"}, Status: MemoryStatusPending}, Embedding: []float32{1, 0, 0, 0}},
		{Entry: MemoryEntry{ID: "m3", Question: "q3", Answer: "a3", Tags: nil, Status: MemoryStatusPending}, Embedding: []float32{1, 0, 0, 0}},
	}
	if err := storage.StoreMemoryBatch(ctx, entries); err != nil {
		t.Fatalf("StoreMemoryBatch: %v", err)
	}

	listed, err := storage.ListMemory(ctx, MemoryListOptions{Tags: []string{"GO"}})
	if err != nil {
		t.Fatalf("ListMemory: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != "m1" {
		t.Fatalf("ListMemory by tag returned %v, want only m1", listed)
	}

	searched, err := storage.SearchMemory(ctx, []float32{1, 0, 0, 0}, MemorySearchOptions{TopK: 10, Tags: []string{"python"}})
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(searched) != 1 || searched[0].Entry.ID != "m2" {
		t.Fatalf("SearchMemory by tag returned %v, want only m2", searched)
	}
}

// TestQdrantStorage_MemoryRoundTripsListFields pins that the comma-joined
// payload encoding of tags, sprints and files survives a round trip.
func TestQdrantStorage_MemoryRoundTripsListFields(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "memfields", 4)
	ctx := context.Background()

	entry := MemoryEntry{
		ID:          "mf-1",
		Question:    "why",
		Answer:      "because",
		Tags:        []string{"a", "b"},
		Source:      "sprint-9",
		Status:      MemoryStatusPending,
		Occurrences: 3,
		CreatedAt:   "2026-01-01T00:00:00Z",
		UpdatedAt:   "2026-01-02T00:00:00Z",
		FilePath:    "/notes.md",
		Sprints:     []string{"9.1", "9.2"},
		Files:       []string{"/a.go", "/b.go"},
	}
	if err := storage.StoreMemory(ctx, entry, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("StoreMemory: %v", err)
	}

	got, err := storage.GetMemory(ctx, "mf-1")
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if got.Question != entry.Question || got.Answer != entry.Answer || got.Source != entry.Source {
		t.Errorf("round trip lost scalar fields: %+v", got)
	}
	if got.Occurrences != 3 {
		t.Errorf("Occurrences = %d, want 3", got.Occurrences)
	}
	if strings.Join(got.Tags, ",") != "a,b" {
		t.Errorf("Tags = %v, want [a b]", got.Tags)
	}
	if strings.Join(got.Sprints, ",") != "9.1,9.2" {
		t.Errorf("Sprints = %v, want [9.1 9.2]", got.Sprints)
	}
	if strings.Join(got.Files, ",") != "/a.go,/b.go" {
		t.Errorf("Files = %v, want [/a.go /b.go]", got.Files)
	}
	if got.FilePath != "/notes.md" {
		t.Errorf("FilePath = %q, want /notes.md", got.FilePath)
	}
}

// TestQdrantStorage_MemoryQueriesIgnoreChunks pins that the entry_type filter
// keeps ordinary chunks out of memory results.
func TestQdrantStorage_MemoryQueriesIgnoreChunks(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "memisolation", 4)
	ctx := context.Background()

	if err := storage.Create(ctx, Chunk{ID: "c-1", FilePath: "/a.go", Type: ChunkFunction, Language: "go"}, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := storage.StoreMemory(ctx, MemoryEntry{ID: "m-1", Question: "q", Answer: "a", Status: MemoryStatusPending}, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("StoreMemory: %v", err)
	}

	listed, err := storage.ListMemory(ctx, MemoryListOptions{})
	if err != nil {
		t.Fatalf("ListMemory: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != "m-1" {
		t.Errorf("ListMemory = %v, want only the memory entry", listed)
	}

	searched, err := storage.SearchMemory(ctx, []float32{1, 0, 0, 0}, MemorySearchOptions{TopK: 10})
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(searched) != 1 || searched[0].Entry.ID != "m-1" {
		t.Errorf("SearchMemory = %v, want only the memory entry", searched)
	}
}

// TestQdrantStorage_ListMemoryOffsetAndLimit pins pagination over the
// tag-filtered entry list.
func TestQdrantStorage_ListMemoryOffsetAndLimit(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "mempaging", 4)
	ctx := context.Background()

	var batch []MemoryWithEmbedding
	for i := 0; i < 6; i++ {
		batch = append(batch, MemoryWithEmbedding{
			Entry:     MemoryEntry{ID: fmt.Sprintf("mp-%d", i), Question: "q", Answer: "a", Status: MemoryStatusPending},
			Embedding: []float32{1, 0, 0, 0},
		})
	}
	if err := storage.StoreMemoryBatch(ctx, batch); err != nil {
		t.Fatalf("StoreMemoryBatch: %v", err)
	}

	page, err := storage.ListMemory(ctx, MemoryListOptions{Offset: 2, Limit: 3})
	if err != nil {
		t.Fatalf("ListMemory: %v", err)
	}
	if len(page) != 3 {
		t.Fatalf("ListMemory returned %d entries, want 3", len(page))
	}
	if page[0].ID != "mp-2" || page[2].ID != "mp-4" {
		t.Errorf("page = %s..%s, want mp-2..mp-4", page[0].ID, page[2].ID)
	}
}

// TestQdrantStorage_SearchMemoryOverRequestsWhenTagFiltering pins the 3x
// over-request that compensates for post-search tag filtering.
func TestQdrantStorage_SearchMemoryOverRequestsWhenTagFiltering(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "memover", 4)
	ctx := context.Background()

	if _, err := storage.SearchMemory(ctx, []float32{1, 0, 0, 0}, MemorySearchOptions{TopK: 5}); err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	fq.mu.Lock()
	plain := fq.lastQueryLimit
	fq.mu.Unlock()
	if plain != 5 {
		t.Errorf("SearchMemory without tags sent limit %d, want 5", plain)
	}

	if _, err := storage.SearchMemory(ctx, []float32{1, 0, 0, 0}, MemorySearchOptions{TopK: 5, Tags: []string{"x"}}); err != nil {
		t.Fatalf("SearchMemory with tags: %v", err)
	}
	fq.mu.Lock()
	tagged := fq.lastQueryLimit
	fq.mu.Unlock()
	if tagged != 15 {
		t.Errorf("SearchMemory with tags sent limit %d, want 15", tagged)
	}
}

// TestQdrantStorage_DeleteMemoryNotFound pins the missing-entry error.
func TestQdrantStorage_DeleteMemoryNotFound(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "memdel", 4)

	if err := storage.DeleteMemory(context.Background(), "absent"); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("DeleteMemory of a missing entry = %v, want ErrMemoryNotFound", err)
	}
}

// TestQdrantStorage_GetMemoryRejectsNonMemoryPoint pins that a point id that
// happens to exist but is not a memory entry is reported as not found.
func TestQdrantStorage_GetMemoryRejectsNonMemoryPoint(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "memtypecheck", 4)
	ctx := context.Background()

	// Plant a point at the memory id whose payload lacks entry_type:"memory".
	fq.mu.Lock()
	id := qdrant.NewID(memoryPointID("impostor"))
	fq.collections["memtypecheck"].points[pointKey(id)] = &fakePoint{
		id:      id,
		vector:  []float32{1, 0, 0, 0},
		payload: map[string]*qdrant.Value{"entry_type": qdrant.NewValueString("retrieval_log")},
	}
	fq.mu.Unlock()

	if _, err := storage.GetMemory(ctx, "impostor"); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("GetMemory on a non-memory point = %v, want ErrMemoryNotFound", err)
	}
}

// TestQdrantStorage_TrackMemoryRetrievalAccumulates pins the iterative mean
// and count kept on the memory point, and the log entry written beside it.
func TestQdrantStorage_TrackMemoryRetrievalAccumulates(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "tracking", 4)
	ctx := context.Background()

	entry := MemoryEntry{ID: "tk-1", Question: "why", Answer: "because", Status: MemoryStatusPending, CreatedAt: "2026-01-01T00:00:00Z", Tags: []string{"a", "b"}}
	if err := storage.StoreMemory(ctx, entry, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("StoreMemory: %v", err)
	}

	if err := storage.TrackMemoryRetrieval(ctx, "tk-1", "first query", 0.6); err != nil {
		t.Fatalf("TrackMemoryRetrieval: %v", err)
	}
	if err := storage.TrackMemoryRetrieval(ctx, "tk-1", "second query", 0.8); err != nil {
		t.Fatalf("TrackMemoryRetrieval: %v", err)
	}

	stats, err := storage.GetMemoryStats(ctx, "tk-1")
	if err != nil {
		t.Fatalf("GetMemoryStats: %v", err)
	}
	if stats.RetrievalCount != 2 {
		t.Errorf("RetrievalCount = %d, want 2", stats.RetrievalCount)
	}
	if diff := stats.AvgScore - 0.7; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("AvgScore = %v, want 0.7", stats.AvgScore)
	}
	if stats.MemoryID != "tk-1" || stats.Question != "why" {
		t.Errorf("stats identity = %+v, want tk-1/why", stats)
	}
	if stats.Status != string(MemoryStatusPending) {
		t.Errorf("Status = %q, want %q", stats.Status, MemoryStatusPending)
	}
	if strings.Join(stats.Tags, ",") != "a,b" {
		t.Errorf("Tags = %v, want [a b]", stats.Tags)
	}
	if stats.LastRetrieved == "" {
		t.Error("LastRetrieved is empty after tracking")
	}

	history, err := storage.GetMemoryRetrievalHistory(ctx, "tk-1", 10)
	if err != nil {
		t.Fatalf("GetMemoryRetrievalHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history has %d entries, want 2", len(history))
	}
	queries := map[string]bool{history[0].Query: true, history[1].Query: true}
	if !queries["first query"] || !queries["second query"] {
		t.Errorf("history queries = %v, want both tracked queries", queries)
	}
	for _, h := range history {
		if h.MemoryID != "tk-1" {
			t.Errorf("history entry memory id = %q, want tk-1", h.MemoryID)
		}
		if h.Timestamp == "" {
			t.Error("history entry has no timestamp")
		}
	}
}

// TestQdrantStorage_TrackMemoryRetrievalMissingMemory pins that tracking an
// unknown memory is an error naming the id.
func TestQdrantStorage_TrackMemoryRetrievalMissingMemory(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "trackmissing", 4)

	err := storage.TrackMemoryRetrieval(context.Background(), "ghost", "q", 0.5)
	if err == nil {
		t.Fatal("expected tracking an unknown memory to fail")
	}
	if !strings.Contains(err.Error(), "memory point not found: ghost") {
		t.Errorf("error = %q, want it to name the missing memory", err)
	}
}

// TestQdrantStorage_TrackMemoryRetrievalBatchStopsOnError pins that a batch
// aborts on the first failing retrieval instead of pressing on.
func TestQdrantStorage_TrackMemoryRetrievalBatchStopsOnError(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "trackbatch", 4)
	ctx := context.Background()

	if err := storage.StoreMemory(ctx, MemoryEntry{ID: "tb-1", Question: "q", Answer: "a", Status: MemoryStatusPending}, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("StoreMemory: %v", err)
	}

	err := storage.TrackMemoryRetrievalBatch(ctx, []MemoryRetrieval{
		{MemoryID: "tb-1", Score: 0.5},
		{MemoryID: "tb-missing", Score: 0.9},
	}, "batch query")
	if err == nil {
		t.Fatal("expected the batch to fail on the unknown memory")
	}
	if !strings.Contains(err.Error(), "tb-missing") {
		t.Errorf("error = %q, want it to name the missing memory", err)
	}

	// The first retrieval still landed.
	stats, statsErr := storage.GetMemoryStats(ctx, "tb-1")
	if statsErr != nil {
		t.Fatalf("GetMemoryStats: %v", statsErr)
	}
	if stats.RetrievalCount != 1 {
		t.Errorf("RetrievalCount = %d, want the first retrieval to have been recorded", stats.RetrievalCount)
	}
}

// TestQdrantStorage_TrackMemoryRetrievalBatchSucceeds pins the happy path over
// several memories sharing one query.
func TestQdrantStorage_TrackMemoryRetrievalBatchSucceeds(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "trackbatchok", 4)
	ctx := context.Background()

	for _, id := range []string{"bb-1", "bb-2"} {
		if err := storage.StoreMemory(ctx, MemoryEntry{ID: id, Question: "q", Answer: "a", Status: MemoryStatusPending}, []float32{1, 0, 0, 0}); err != nil {
			t.Fatalf("StoreMemory %s: %v", id, err)
		}
	}

	err := storage.TrackMemoryRetrievalBatch(ctx, []MemoryRetrieval{
		{MemoryID: "bb-1", Score: 0.4},
		{MemoryID: "bb-2", Score: 0.9},
	}, "shared query")
	if err != nil {
		t.Fatalf("TrackMemoryRetrievalBatch: %v", err)
	}

	all, err := storage.GetAllMemoryStats(ctx)
	if err != nil {
		t.Fatalf("GetAllMemoryStats: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("GetAllMemoryStats returned %d rows, want 2", len(all))
	}
	byID := map[string]RetrievalStats{}
	for _, s := range all {
		byID[s.MemoryID] = s
	}
	if byID["bb-1"].RetrievalCount != 1 || byID["bb-2"].RetrievalCount != 1 {
		t.Errorf("stats = %+v, want one retrieval each", byID)
	}
	if diff := byID["bb-2"].AvgScore - 0.9; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("bb-2 AvgScore = %v, want 0.9", byID["bb-2"].AvgScore)
	}
}

// TestQdrantStorage_GetMemoryStatsNotFound pins the missing-entry error on the
// stats read.
func TestQdrantStorage_GetMemoryStatsNotFound(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "statsmissing", 4)

	if _, err := storage.GetMemoryStats(context.Background(), "nope"); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("GetMemoryStats = %v, want ErrMemoryNotFound", err)
	}
}

// TestQdrantStorage_GetMemoryRetrievalHistoryOrdersAndTrims pins that history
// comes back newest first and respects the caller's limit.
func TestQdrantStorage_GetMemoryRetrievalHistoryOrdersAndTrims(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "history", 4)
	ctx := context.Background()

	if err := storage.StoreMemory(ctx, MemoryEntry{ID: "h-1", Question: "q", Answer: "a", Status: MemoryStatusPending}, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("StoreMemory: %v", err)
	}

	// Plant three logs with known, distinct timestamps.
	stamps := []string{"2026-01-01T00:00:00Z", "2026-03-01T00:00:00Z", "2026-02-01T00:00:00Z"}
	fq.mu.Lock()
	for i, ts := range stamps {
		id := qdrant.NewID(stringToUUID(fmt.Sprintf("planted-log-%d", i)))
		fq.collections["history"].points[pointKey(id)] = &fakePoint{
			id:     id,
			vector: []float32{0, 0, 0, 0},
			seq:    uint64(100 + i),
			payload: map[string]*qdrant.Value{
				"entry_type": qdrant.NewValueString("retrieval_log"),
				"memory_id":  qdrant.NewValueString("h-1"),
				"query":      qdrant.NewValueString(ts),
				"score":      NewValueFloat(float64(i) / 10),
				"timestamp":  qdrant.NewValueString(ts),
			},
		}
	}
	fq.mu.Unlock()

	history, err := storage.GetMemoryRetrievalHistory(ctx, "h-1", 2)
	if err != nil {
		t.Fatalf("GetMemoryRetrievalHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history has %d entries, want the limit of 2", len(history))
	}
	if history[0].Timestamp != "2026-03-01T00:00:00Z" {
		t.Errorf("newest entry = %q, want the March log", history[0].Timestamp)
	}
	if history[1].Timestamp != "2026-02-01T00:00:00Z" {
		t.Errorf("second entry = %q, want the February log", history[1].Timestamp)
	}
}

// TestQdrantStorage_PruneMemoryRetrievalLog pins that pruning removes only the
// logs older than the cutoff and reports the count.
func TestQdrantStorage_PruneMemoryRetrievalLog(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "prune", 4)
	ctx := context.Background()

	// Two ancient logs and one that is still current.
	entries := []struct {
		id string
		ts string
	}{
		{"old-1", "2020-01-01T00:00:00Z"},
		{"old-2", "2020-06-01T00:00:00Z"},
		{"new-1", "2099-01-01T00:00:00Z"},
	}
	fq.mu.Lock()
	for i, e := range entries {
		id := qdrant.NewID(stringToUUID(e.id))
		fq.collections["prune"].points[pointKey(id)] = &fakePoint{
			id:     id,
			vector: []float32{0, 0, 0, 0},
			seq:    uint64(10 + i),
			payload: map[string]*qdrant.Value{
				"entry_type": qdrant.NewValueString("retrieval_log"),
				"memory_id":  qdrant.NewValueString("m"),
				"timestamp":  qdrant.NewValueString(e.ts),
			},
		}
	}
	fq.mu.Unlock()

	n, err := storage.PruneMemoryRetrievalLog(ctx, 30)
	if err != nil {
		t.Fatalf("PruneMemoryRetrievalLog: %v", err)
	}
	if n != 2 {
		t.Fatalf("PruneMemoryRetrievalLog removed %d, want 2", n)
	}
	if got := fq.pointCount("prune"); got != 1 {
		t.Errorf("collection holds %d points after pruning, want 1", got)
	}
	if fq.payloadOf("prune", pointKey(qdrant.NewID(stringToUUID("new-1")))) == nil {
		t.Error("pruning removed the current log entry")
	}
}

// TestQdrantStorage_PruneMemoryRetrievalLogNothingToDo pins the zero-work path.
func TestQdrantStorage_PruneMemoryRetrievalLogNothingToDo(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "prune_none", 4)

	before := fq.callCount("DeletePoints")
	n, err := storage.PruneMemoryRetrievalLog(context.Background(), 30)
	if err != nil {
		t.Fatalf("PruneMemoryRetrievalLog: %v", err)
	}
	if n != 0 {
		t.Errorf("PruneMemoryRetrievalLog = %d, want 0", n)
	}
	if fq.callCount("DeletePoints") != before {
		t.Error("pruning with nothing to prune still issued a delete")
	}
}

// TestQdrantStorage_UpdateMemoryStatsStatus pins that the status change is
// written to the memory point and visible to later reads.
func TestQdrantStorage_UpdateMemoryStatsStatus(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "statusupdate", 4)
	ctx := context.Background()

	if err := storage.StoreMemory(ctx, MemoryEntry{ID: "su-1", Question: "q", Answer: "a", Status: MemoryStatusPending}, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("StoreMemory: %v", err)
	}

	if err := storage.UpdateMemoryStatsStatus(ctx, "su-1", "archived"); err != nil {
		t.Fatalf("UpdateMemoryStatsStatus: %v", err)
	}

	entry, err := storage.GetMemory(ctx, "su-1")
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if string(entry.Status) != "archived" {
		t.Errorf("Status = %q, want archived", entry.Status)
	}

	if err := storage.UpdateMemoryStatsStatus(ctx, "absent", "archived"); !errors.Is(err, ErrMemoryNotFound) {
		t.Errorf("UpdateMemoryStatsStatus on a missing entry = %v, want ErrMemoryNotFound", err)
	}
}

// TestQdrantStorage_ListIndexedFilesPaginates pins that the file-hash listing
// keeps scrolling past its 500-point page size.
func TestQdrantStorage_ListIndexedFilesPaginates(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "indexedfiles", 4)
	ctx := context.Background()

	const n = 1100
	for i := 0; i < n; i++ {
		if err := storage.SetFileHash(ctx, fmt.Sprintf("/f%04d.go", i), fmt.Sprintf("h%04d", i)); err != nil {
			t.Fatalf("SetFileHash: %v", err)
		}
	}

	files, err := storage.ListIndexedFiles(ctx)
	if err != nil {
		t.Fatalf("ListIndexedFiles: %v", err)
	}
	if len(files) != n {
		t.Fatalf("ListIndexedFiles returned %d files, want %d", len(files), n)
	}
	if !files["/f1099.go"] {
		t.Error("ListIndexedFiles missed a file on a later page")
	}
}

// TestQdrantStorage_GetFileHashWrapsFailure pins the error wrapping when the
// hash lookup itself fails, distinct from the "not indexed" empty string.
func TestQdrantStorage_GetFileHashWrapsFailure(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "hashfail", 4)
	fq.setFailure("GetPoints", status.Error(codes.Unavailable, "down"))

	_, err := storage.GetFileHash(context.Background(), "/a.go")
	if err == nil {
		t.Fatal("expected GetFileHash to fail when the point lookup fails")
	}
	if !strings.Contains(err.Error(), "failed to get file hash") {
		t.Errorf("error = %q, want it to mention the file hash", err)
	}
}

// TestQdrantStorage_NewQdrantStorageFromEnv pins that the env constructor
// reads URL, key and collection from the environment and defaults the
// collection name when it is unset.
func TestQdrantStorage_NewQdrantStorageFromEnv(t *testing.T) {
	fq, url := startFakeQdrant(t)

	t.Setenv("QDRANT_API_URL", url)
	t.Setenv("QDRANT_API_KEY", "env-key")
	t.Setenv("QDRANT_COLLECTION", "  from_env  ")

	storage, err := NewQdrantStorageFromEnv(4)
	if err == nil {
		defer storage.Close()
		t.Fatal("expected NewQdrantStorageFromEnv to fail without an FTS data dir")
	}
	if !strings.Contains(err.Error(), "FTSDataDir is required") {
		t.Fatalf("error = %q, want the FTS path requirement", err)
	}
	// The collection name was trimmed and the collection really was created
	// before the FTS index failed.
	if !fq.hasCollection("from_env") {
		t.Error("expected the trimmed collection name to be used")
	}

	t.Setenv("QDRANT_COLLECTION", "")
	if _, err := NewQdrantStorageFromEnv(4); err == nil {
		t.Fatal("expected the default-collection run to fail on the FTS path too")
	}
	if !fq.hasCollection("llm_semantic") {
		t.Error("expected the default collection name when QDRANT_COLLECTION is empty")
	}
}

// TestQdrantStorage_RefsDelegateToParallelIndex pins that reference storage is
// served by the parallel SQLite index, since Qdrant cannot express the join.
func TestQdrantStorage_RefsDelegateToParallelIndex(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "refs", 4)
	ctx := context.Background()

	caller := Chunk{ID: "caller", FilePath: "/a.go", Type: ChunkFunction, Name: "Caller", Content: "func Caller() { Callee() }", Language: "go", StartLine: 1, EndLine: 3}
	callee := Chunk{ID: "callee", FilePath: "/b.go", Type: ChunkFunction, Name: "Callee", Content: "func Callee() {}", Language: "go", StartLine: 1, EndLine: 2}
	for _, c := range []Chunk{caller, callee} {
		if err := storage.Create(ctx, c, []float32{1, 0, 0, 0}); err != nil {
			t.Fatalf("Create %s: %v", c.ID, err)
		}
	}

	refs := []ChunkRef{{ChunkID: "caller", RefType: RefCalls, RefName: "Callee"}}
	if err := storage.StoreRefs(ctx, refs); err != nil {
		t.Fatalf("StoreRefs: %v", err)
	}
	if err := storage.ResolveRefs(ctx); err != nil {
		t.Fatalf("ResolveRefs: %v", err)
	}

	out, err := storage.GetRefs(ctx, "caller")
	if err != nil {
		t.Fatalf("GetRefs: %v", err)
	}
	if len(out) != 1 || out[0].RefName != "Callee" {
		t.Fatalf("GetRefs = %v, want one edge to Callee", out)
	}
	if out[0].RefTargetID != "callee" {
		t.Errorf("ResolveRefs left RefTargetID = %q, want callee", out[0].RefTargetID)
	}

	callers, err := storage.GetCallers(ctx, "callee")
	if err != nil {
		t.Fatalf("GetCallers: %v", err)
	}
	if len(callers) != 1 || callers[0].ChunkID != "caller" {
		t.Fatalf("GetCallers = %v, want the caller edge", callers)
	}

	byName, err := storage.GetCallersByName(ctx, "Callee")
	if err != nil {
		t.Fatalf("GetCallersByName: %v", err)
	}
	if len(byName) != 1 || byName[0].Name != "Caller" {
		t.Fatalf("GetCallersByName = %v, want the Caller edge", byName)
	}

	outByName, err := storage.GetRefsByName(ctx, "Caller")
	if err != nil {
		t.Fatalf("GetRefsByName: %v", err)
	}
	if len(outByName) != 1 || outByName[0].RefName != "Callee" {
		t.Fatalf("GetRefsByName = %v, want the outgoing Callee edge", outByName)
	}

	if err := storage.DeleteRefsByChunk(ctx, "caller"); err != nil {
		t.Fatalf("DeleteRefsByChunk: %v", err)
	}
	after, err := storage.GetRefs(ctx, "caller")
	if err != nil {
		t.Fatalf("GetRefs after delete: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("GetRefs after delete = %v, want none", after)
	}
}

// TestStringToUUID pins that the chunk-id hash is a stable, well-formed UUID
// and that distinct inputs do not collide.
func TestStringToUUID(t *testing.T) {
	a := stringToUUID("chunk-one")
	if a != stringToUUID("chunk-one") {
		t.Error("stringToUUID is not deterministic")
	}
	if a == stringToUUID("chunk-two") {
		t.Error("stringToUUID collided on distinct inputs")
	}

	parts := strings.Split(a, "-")
	if len(parts) != 5 {
		t.Fatalf("stringToUUID(%q) = %q, want five dash-separated groups", "chunk-one", a)
	}
	for i, want := range []int{8, 4, 4, 4, 12} {
		if len(parts[i]) != want {
			t.Errorf("group %d = %q (%d chars), want %d", i, parts[i], len(parts[i]), want)
		}
	}

	// The three id namespaces must not overlap for the same key.
	key := "same"
	ids := map[string]string{
		"chunk":     stringToUUID(key),
		"memory":    memoryPointID(key),
		"file_hash": fileHashID(key),
	}
	seen := map[string]string{}
	for ns, id := range ids {
		if other, dup := seen[id]; dup {
			t.Errorf("namespace %s collides with %s on key %q", ns, other, key)
		}
		seen[id] = ns
	}
	if calibrationMetadataID() != stringToUUID("calibration_metadata") {
		t.Error("calibrationMetadataID drifted from its namespace string")
	}
}

// TestNewValueFloat pins the double-valued payload helper used by the stats
// tracker.
func TestNewValueFloat(t *testing.T) {
	v := NewValueFloat(0.125)
	if v.GetDoubleValue() != 0.125 {
		t.Errorf("NewValueFloat(0.125).GetDoubleValue() = %v, want 0.125", v.GetDoubleValue())
	}
	// It must be a double, not an integer: reading it as an int yields zero.
	if v.GetIntegerValue() != 0 {
		t.Errorf("NewValueFloat produced an integer value: %v", v.GetIntegerValue())
	}
}

// TestPayloadToChunk pins the payload decoding in isolation, including the
// UUID fallback when chunk_id is absent and the "code" domain default.
func TestPayloadToChunk(t *testing.T) {
	t.Run("nil payload yields a zero chunk", func(t *testing.T) {
		got := payloadToChunk(nil, qdrant.NewID("ignored"))
		if got.ID != "" || got.Domain != "" {
			t.Errorf("payloadToChunk(nil) = %+v, want the zero Chunk", got)
		}
	})

	t.Run("full payload", func(t *testing.T) {
		payload := map[string]*qdrant.Value{
			"chunk_id":   qdrant.NewValueString("c-1"),
			"file_path":  qdrant.NewValueString("/a.go"),
			"type":       qdrant.NewValueString(ChunkFunction.String()),
			"name":       qdrant.NewValueString("Fn"),
			"signature":  qdrant.NewValueString("func Fn()"),
			"content":    qdrant.NewValueString("body"),
			"start_line": qdrant.NewValueInt(3),
			"end_line":   qdrant.NewValueInt(9),
			"language":   qdrant.NewValueString("go"),
			"domain":     qdrant.NewValueString("docs"),
		}
		got := payloadToChunk(payload, qdrant.NewID("uuid-here"))
		if got.ID != "c-1" {
			t.Errorf("ID = %q, want the payload chunk_id", got.ID)
		}
		if got.Type != ChunkFunction {
			t.Errorf("Type = %v, want ChunkFunction", got.Type)
		}
		if got.StartLine != 3 || got.EndLine != 9 {
			t.Errorf("lines = %d-%d, want 3-9", got.StartLine, got.EndLine)
		}
		if got.Signature != "func Fn()" || got.Content != "body" || got.Language != "go" {
			t.Errorf("decoded chunk = %+v", got)
		}
		if got.Domain != "docs" {
			t.Errorf("Domain = %q, want docs", got.Domain)
		}
	})

	t.Run("falls back to the point uuid", func(t *testing.T) {
		payload := map[string]*qdrant.Value{"file_path": qdrant.NewValueString("/a.go")}
		got := payloadToChunk(payload, qdrant.NewID("fallback-uuid"))
		if got.ID != "fallback-uuid" {
			t.Errorf("ID = %q, want the point UUID", got.ID)
		}
		if got.Domain != "code" {
			t.Errorf("Domain = %q, want the code default", got.Domain)
		}
	})

	t.Run("falls back to a numeric point id", func(t *testing.T) {
		payload := map[string]*qdrant.Value{"file_path": qdrant.NewValueString("/a.go")}
		got := payloadToChunk(payload, qdrant.NewIDNum(42))
		if got.ID != "42" {
			t.Errorf("ID = %q, want the numeric point id rendered as a string", got.ID)
		}
	})
}

// TestPayloadToMemory pins memory decoding in isolation: non-memory payloads
// are rejected and empty list fields decode to nil rather than [""].
func TestPayloadToMemory(t *testing.T) {
	if payloadToMemory(nil) != nil {
		t.Error("payloadToMemory(nil) should be nil")
	}
	if payloadToMemory(map[string]*qdrant.Value{}) != nil {
		t.Error("a payload with no entry_type is not a memory entry")
	}
	if payloadToMemory(map[string]*qdrant.Value{"entry_type": qdrant.NewValueString("chunk")}) != nil {
		t.Error("a payload whose entry_type is not memory must be rejected")
	}

	got := payloadToMemory(map[string]*qdrant.Value{
		"entry_type": qdrant.NewValueString("memory"),
		"memory_id":  qdrant.NewValueString("m-1"),
		"tags":       qdrant.NewValueString(""),
		"sprints":    qdrant.NewValueString(""),
		"files":      qdrant.NewValueString(""),
	})
	if got == nil {
		t.Fatal("a memory payload must decode")
	}
	if got.ID != "m-1" {
		t.Errorf("ID = %q, want m-1", got.ID)
	}
	if got.Tags != nil || got.Sprints != nil || got.Files != nil {
		t.Errorf("empty list fields decoded to %v/%v/%v, want nil", got.Tags, got.Sprints, got.Files)
	}
}
