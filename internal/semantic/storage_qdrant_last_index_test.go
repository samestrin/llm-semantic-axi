package semantic

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// recordedIndexTime is far enough from the test clock that a Stats call which
// falls back to time.Now() cannot be mistaken for reading the record.
var recordedIndexTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// TestQdrantStorage_LastIndexRoundTrip defends against Qdrant dropping the record
// of the last index: the commit, time and root written are the ones read.
func TestQdrantStorage_LastIndexRoundTrip(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "lastindex_rt", 4)
	ctx := context.Background()

	want := LastIndex{Commit: "0123456789abcdef0123456789abcdef01234567", IndexedAt: recordedIndexTime, Root: "/src/repo"}
	if err := storage.SetLastIndex(ctx, want); err != nil {
		t.Fatalf("SetLastIndex: %v", err)
	}

	got, err := storage.GetLastIndex(ctx)
	if err != nil {
		t.Fatalf("GetLastIndex: %v", err)
	}
	if got == nil {
		t.Fatal("GetLastIndex = nil after SetLastIndex")
	}
	if got.Commit != want.Commit || !got.IndexedAt.Equal(want.IndexedAt) || got.Root != want.Root {
		t.Errorf("GetLastIndex = %+v, want %+v", got, want)
	}
}

// TestQdrantStorage_LastIndexReplaces defends against a stale record winning:
// a second SetLastIndex replaces the first rather than adding a second point.
func TestQdrantStorage_LastIndexReplaces(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "lastindex_replace", 4)
	ctx := context.Background()

	if err := storage.SetLastIndex(ctx, LastIndex{Commit: "aaaa", IndexedAt: recordedIndexTime}); err != nil {
		t.Fatalf("SetLastIndex: %v", err)
	}
	later := recordedIndexTime.Add(time.Hour)
	if err := storage.SetLastIndex(ctx, LastIndex{Commit: "bbbb", IndexedAt: later}); err != nil {
		t.Fatalf("SetLastIndex: %v", err)
	}

	got, err := storage.GetLastIndex(ctx)
	if err != nil || got == nil {
		t.Fatalf("GetLastIndex = %+v, %v", got, err)
	}
	if got.Commit != "bbbb" || !got.IndexedAt.Equal(later) {
		t.Errorf("GetLastIndex = %+v, want commit bbbb at %v", got, later)
	}
	records := fq.payloadsWhere("lastindex_replace", func(p map[string]*qdrant.Value) bool {
		return p["type"].GetStringValue() == "last_index"
	})
	if len(records) != 1 {
		t.Errorf("last_index points = %d, want 1", len(records))
	}
}

// TestQdrantStorage_LastIndexAbsent defends the "never recorded" answer: a fresh
// collection reads (nil, nil), not an error and not ErrLastIndexUnsupported.
func TestQdrantStorage_LastIndexAbsent(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "lastindex_absent", 4)

	got, err := storage.GetLastIndex(context.Background())
	if err != nil {
		t.Fatalf("GetLastIndex: %v", err)
	}
	if got != nil {
		t.Errorf("GetLastIndex = %+v, want nil before any index", got)
	}
}

// TestQdrantStorage_ClearDropsLastIndex defends against a cleared collection still
// claiming it reflects a commit, as sqlite's Clear already guarantees: if the
// re-index after Clear fails, the old record would sit over a partial index and
// read as fresh. Clear drops the record and the chunks, but keeps
// calibration_metadata.
func TestQdrantStorage_ClearDropsLastIndex(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "lastindex_clear", 4)
	ctx := context.Background()

	if err := storage.SetLastIndex(ctx, LastIndex{Commit: "cafebabe", IndexedAt: recordedIndexTime}); err != nil {
		t.Fatalf("SetLastIndex: %v", err)
	}
	calibration := &CalibrationMetadata{EmbeddingModel: "m", CalibrationDate: recordedIndexTime, HighThreshold: 0.7}
	if err := storage.SetCalibrationMetadata(ctx, calibration); err != nil {
		t.Fatalf("SetCalibrationMetadata: %v", err)
	}
	if err := storage.Create(ctx, Chunk{ID: "li-1", FilePath: "/a.go", Type: ChunkFunction, Language: "go"}, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := storage.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	if _, err := storage.Read(ctx, "li-1"); err == nil {
		t.Error("chunk survived Clear")
	}
	got, err := storage.GetLastIndex(ctx)
	if err != nil {
		t.Fatalf("GetLastIndex: %v", err)
	}
	if got != nil {
		t.Errorf("GetLastIndex after Clear = %+v, want nil", got)
	}
	meta, err := storage.GetCalibrationMetadata(ctx)
	if err != nil {
		t.Fatalf("GetCalibrationMetadata: %v", err)
	}
	if meta == nil || meta.EmbeddingModel != calibration.EmbeddingModel || meta.HighThreshold != calibration.HighThreshold {
		t.Errorf("calibration after Clear = %+v, want %+v", meta, calibration)
	}
}

// TestQdrantStorage_StatsLastUpdatedIsRecordedIndexTime defends against Stats
// reporting the moment it was asked as the moment the index was built: an index
// last built months ago must not read as updated just now.
func TestQdrantStorage_StatsLastUpdatedIsRecordedIndexTime(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "lastindex_stats", 4)
	ctx := context.Background()

	if err := storage.SetLastIndex(ctx, LastIndex{Commit: "cafebabe", IndexedAt: recordedIndexTime}); err != nil {
		t.Fatalf("SetLastIndex: %v", err)
	}

	stats, err := storage.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if want := recordedIndexTime.Format(time.RFC3339); stats.LastUpdated != want {
		t.Errorf("Stats LastUpdated = %q, want the recorded index time %q", stats.LastUpdated, want)
	}
	if parsed, err := time.Parse(time.RFC3339, stats.LastUpdated); err == nil && time.Since(parsed) < time.Hour {
		t.Errorf("Stats LastUpdated = %q is the current time, not the recorded index time", stats.LastUpdated)
	}
}

// TestQdrantStorage_StatsLastUpdatedEmptyWithoutRecord defends against a
// never-recorded index reading as freshly updated: with no record, LastUpdated
// is empty (as sqlite reports for an empty index), not time.Now().
func TestQdrantStorage_StatsLastUpdatedEmptyWithoutRecord(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "lastindex_stats_none", 4)

	stats, err := storage.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.LastUpdated != "" {
		t.Errorf("Stats LastUpdated = %q, want empty when no index has been recorded", stats.LastUpdated)
	}
}

// TestQdrantStorage_StatsFailsWhenRecordUnreadable defends against an unreadable
// record being reported as "never indexed": Stats returns the error instead of an
// empty LastUpdated.
func TestQdrantStorage_StatsFailsWhenRecordUnreadable(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "lastindex_stats_fail", 4)
	fq.setFailure("GetPoints", status.Error(codes.Unavailable, "no node"))

	_, err := storage.Stats(context.Background())
	if err == nil {
		t.Fatal("expected Stats to fail when the last index record cannot be read")
	}
	if !strings.Contains(err.Error(), "last index") {
		t.Errorf("error = %q, want it to mention the last index", err)
	}
}

// TestQdrantStorage_GetLastIndexWrapsServerFailure defends against a failed read
// looking like "never indexed" (nil, nil).
func TestQdrantStorage_GetLastIndexWrapsServerFailure(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "lastindex_getfail", 4)
	fq.setFailure("GetPoints", status.Error(codes.Unavailable, "no node"))

	got, err := storage.GetLastIndex(context.Background())
	if err == nil {
		t.Fatalf("GetLastIndex = %+v, nil; want an error when the server fails", got)
	}
	if !strings.Contains(err.Error(), "failed to get last index") {
		t.Errorf("error = %q, want it to mention the last index", err)
	}
}

// TestQdrantStorage_SetLastIndexWrapsServerFailure defends against a dropped
// write being reported as recorded.
func TestQdrantStorage_SetLastIndexWrapsServerFailure(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "lastindex_setfail", 4)
	fq.setFailure("Upsert", status.Error(codes.Unavailable, "no node"))

	err := storage.SetLastIndex(context.Background(), LastIndex{Commit: "aaaa", IndexedAt: recordedIndexTime})
	if err == nil {
		t.Fatal("expected SetLastIndex to fail when the server fails")
	}
	if !strings.Contains(err.Error(), "failed to set last index") {
		t.Errorf("error = %q, want it to mention the last index", err)
	}
}

// TestQdrantStorage_GetLastIndexRejectsCorruptTime defends against garbage in the
// record being read as a valid zero time.
func TestQdrantStorage_GetLastIndexRejectsCorruptTime(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "lastindex_corrupt", 4)
	ctx := context.Background()

	_, err := fq.Upsert(ctx, &qdrant.UpsertPoints{
		CollectionName: "lastindex_corrupt",
		Points: []*qdrant.PointStruct{{
			Id:      qdrant.NewID(lastIndexID()),
			Vectors: qdrant.NewVectors(0, 0, 0, 0),
			Payload: map[string]*qdrant.Value{
				"type":       qdrant.NewValueString("last_index"),
				"commit":     qdrant.NewValueString("aaaa"),
				"indexed_at": qdrant.NewValueString("not a time"),
			},
		}},
	})
	if err != nil {
		t.Fatalf("seed corrupt record: %v", err)
	}

	if got, err := storage.GetLastIndex(ctx); err == nil {
		t.Errorf("GetLastIndex = %+v, nil; want an error for a corrupt indexed_at", got)
	}
}

// TestQdrantStorage_LastIndexClosed defends against a closed store answering
// "never indexed".
func TestQdrantStorage_LastIndexClosed(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "lastindex_closed", 4)
	if err := storage.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx := context.Background()

	if _, err := storage.GetLastIndex(ctx); err != ErrStorageClosed {
		t.Errorf("GetLastIndex on closed store = %v, want ErrStorageClosed", err)
	}
	if err := storage.SetLastIndex(ctx, LastIndex{}); err != ErrStorageClosed {
		t.Errorf("SetLastIndex on closed store = %v, want ErrStorageClosed", err)
	}
}

// TestQdrantStorage_StatsDoesNotCountLastIndexAsChunk defends the index_empty
// answer: an index that recorded a run but holds no chunks still reports 0
// chunks, so a search over it says the index is empty rather than "no match".
func TestQdrantStorage_StatsDoesNotCountLastIndexAsChunk(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "lastindex_nochunks", 4)
	ctx := context.Background()

	if err := storage.SetLastIndex(ctx, LastIndex{Commit: "aaaa", IndexedAt: recordedIndexTime}); err != nil {
		t.Fatalf("SetLastIndex: %v", err)
	}

	stats, err := storage.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ChunksTotal != 0 {
		t.Errorf("Stats ChunksTotal = %d, want 0: the last_index record is not a chunk", stats.ChunksTotal)
	}
}

// TestQdrantStorage_StatsUncountedCollectionWithLastIndex defends the file
// count against the points that share the collection with chunks. File hashes
// and memories carry a file_path too, so counting every point reported files
// that have no chunk (a file whose hash was stored but whose chunks were not,
// or a memory's source file). The server here reports no point count, so
// nothing can come from PointsCount either: both totals are chunk-only.
func TestQdrantStorage_StatsUncountedCollectionWithLastIndex(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "lastindex_uncounted", 4)
	ctx := context.Background()

	if err := storage.SetLastIndex(ctx, LastIndex{Commit: "aaaa", IndexedAt: recordedIndexTime}); err != nil {
		t.Fatalf("SetLastIndex: %v", err)
	}
	if err := storage.SetCalibrationMetadata(ctx, &CalibrationMetadata{EmbeddingModel: "m", CalibrationDate: recordedIndexTime}); err != nil {
		t.Fatalf("SetCalibrationMetadata: %v", err)
	}
	if err := storage.SetFileHash(ctx, "/hashed-only.go", "hash"); err != nil {
		t.Fatalf("SetFileHash: %v", err)
	}
	memory := MemoryEntry{ID: "mem-1", Question: "q", Answer: "a", Status: MemoryStatusPending, FilePath: "/memory-source.md"}
	if err := storage.StoreMemory(ctx, memory, []float32{0, 1, 0, 0}); err != nil {
		t.Fatalf("StoreMemory: %v", err)
	}
	chunks := []Chunk{
		{ID: "c-a1", FilePath: "/a.go", Type: ChunkFunction, Name: "A1", Content: "func A1() {}", Language: "go"},
		{ID: "c-a2", FilePath: "/a.go", Type: ChunkFunction, Name: "A2", Content: "func A2() {}", Language: "go"},
		{ID: "c-b1", FilePath: "/b.go", Type: ChunkFunction, Name: "B1", Content: "func B1() {}", Language: "go"},
	}
	for _, c := range chunks {
		if err := storage.Create(ctx, c, []float32{1, 0, 0, 0}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := storage.SetFileHash(ctx, c.FilePath, "hash"); err != nil {
			t.Fatalf("SetFileHash: %v", err)
		}
	}
	fq.mu.Lock()
	fq.omitPointsCount = true
	fq.mu.Unlock()

	stats, err := storage.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.FilesIndexed != 2 {
		t.Errorf("Stats FilesIndexed = %d, want 2: only /a.go and /b.go have chunks", stats.FilesIndexed)
	}
	if stats.ChunksTotal != 3 {
		t.Errorf("Stats ChunksTotal = %d, want 3 when the server reports no point count", stats.ChunksTotal)
	}
	if want := recordedIndexTime.Format(time.RFC3339); stats.LastUpdated != want {
		t.Errorf("Stats LastUpdated = %q, want %q", stats.LastUpdated, want)
	}
}

// TestQdrantStorage_StatsCountsOnlyChunkPoints defends the index_empty answer
// against the points that share the collection with chunks: file hashes,
// calibration metadata, memory entries and the last_index record are not
// chunks. Counting them made a calibrated index with no indexable files report
// chunks, so search answered no_match instead of index_empty.
func TestQdrantStorage_StatsCountsOnlyChunkPoints(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "stats_chunks_only", 4)
	ctx := context.Background()

	if err := storage.SetCalibrationMetadata(ctx, &CalibrationMetadata{EmbeddingModel: "m", CalibrationDate: recordedIndexTime}); err != nil {
		t.Fatalf("SetCalibrationMetadata: %v", err)
	}
	if err := storage.SetFileHash(ctx, "/empty.go", "hash"); err != nil {
		t.Fatalf("SetFileHash: %v", err)
	}
	memory := MemoryEntry{ID: "mem-1", Question: "q", Answer: "a", Status: MemoryStatusPending}
	if err := storage.StoreMemory(ctx, memory, []float32{0, 1, 0, 0}); err != nil {
		t.Fatalf("StoreMemory: %v", err)
	}
	if err := storage.SetLastIndex(ctx, LastIndex{Commit: "aaaa", IndexedAt: recordedIndexTime}); err != nil {
		t.Fatalf("SetLastIndex: %v", err)
	}

	stats, err := storage.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ChunksTotal != 0 {
		t.Errorf("Stats ChunksTotal = %d with no chunks stored, want 0: only chunk points are chunks", stats.ChunksTotal)
	}

	chunk := Chunk{ID: "c-1", FilePath: "/a.go", Type: ChunkFunction, Name: "Alpha", Content: "func Alpha() {}", Language: "go"}
	if err := storage.Create(ctx, chunk, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	stats, err = storage.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ChunksTotal != 1 {
		t.Errorf("Stats ChunksTotal = %d with one chunk stored, want 1", stats.ChunksTotal)
	}
}

// TestQdrantStorage_ContractVersionStamped defends the contract marker: a new
// collection records the storage contract version, so a reader can tell which
// version of docs/storage-contract.md wrote it.
func TestQdrantStorage_ContractVersionStamped(t *testing.T) {
	_, fq := newFakeQdrantStorage(t, "contract_stamp", 4)

	records := fq.payloadsWhere("contract_stamp", func(p map[string]*qdrant.Value) bool {
		return p["type"].GetStringValue() == "storage_contract"
	})
	if len(records) != 1 {
		t.Fatalf("storage_contract points = %d, want 1", len(records))
	}
	if got := records[0]["contract_version"].GetIntegerValue(); got != StorageContractVersion {
		t.Errorf("contract_version = %d, want %d", got, StorageContractVersion)
	}
}

// TestQdrantStorage_ContractVersionMismatchFails defends the clear error: a
// collection stamped with another contract version must refuse to open rather
// than be read as if its payload shapes were the ones this binary writes.
func TestQdrantStorage_ContractVersionMismatchFails(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "contract_mismatch", 4)
	stamp := storage.contractPoint(StorageContractVersion + 1)
	if _, err := storage.client.Upsert(context.Background(), &qdrant.UpsertPoints{
		CollectionName: "contract_mismatch",
		Points:         []*qdrant.PointStruct{stamp},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	err := storage.ensureCollection()
	if err == nil {
		t.Fatal("ensureCollection accepted a collection stamped with another contract version")
	}
	if !strings.Contains(err.Error(), "storage contract version") {
		t.Errorf("error = %q, want it to name the storage contract version", err)
	}
	_ = fq
}

// TestQdrantStorage_ContractVersionStampsLegacyCollection defends collections
// created before the marker existed: they are version 1 by definition, and
// opening one stamps it instead of failing.
func TestQdrantStorage_ContractVersionStampsLegacyCollection(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "contract_legacy", 4)
	if _, err := storage.client.Delete(context.Background(), &qdrant.DeletePoints{
		CollectionName: "contract_legacy",
		Points:         qdrant.NewPointsSelector(qdrant.NewID(contractID())),
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := storage.ensureCollection(); err != nil {
		t.Fatalf("ensureCollection on legacy collection: %v", err)
	}
	if fq.payloadOf("contract_legacy", contractID()) == nil {
		t.Error("legacy collection was not stamped")
	}
}

// TestQdrantStorage_ClearKeepsContractVersion defends --force re-index: Clear
// recreates the collection and must leave it stamped.
func TestQdrantStorage_ClearKeepsContractVersion(t *testing.T) {
	storage, fq := newFakeQdrantStorage(t, "contract_clear", 4)
	if err := storage.Clear(context.Background()); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if fq.payloadOf("contract_clear", contractID()) == nil {
		t.Error("Clear left the collection without a contract version")
	}
}
