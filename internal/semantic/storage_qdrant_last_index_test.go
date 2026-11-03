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
// of the last index: the commit and time written are the commit and time read.
func TestQdrantStorage_LastIndexRoundTrip(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "lastindex_rt", 4)
	ctx := context.Background()

	want := LastIndex{Commit: "0123456789abcdef0123456789abcdef01234567", IndexedAt: recordedIndexTime}
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
	if got.Commit != want.Commit || !got.IndexedAt.Equal(want.IndexedAt) {
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

// TestQdrantStorage_LastIndexSurvivesClear pins that Clear keeps the record, as
// it keeps calibration_metadata, while the chunks go.
func TestQdrantStorage_LastIndexSurvivesClear(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "lastindex_clear", 4)
	ctx := context.Background()

	want := LastIndex{Commit: "cafebabe", IndexedAt: recordedIndexTime}
	if err := storage.SetLastIndex(ctx, want); err != nil {
		t.Fatalf("SetLastIndex: %v", err)
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
	if got == nil {
		t.Fatal("Clear dropped the last index record")
	}
	if got.Commit != want.Commit || !got.IndexedAt.Equal(want.IndexedAt) {
		t.Errorf("GetLastIndex after Clear = %+v, want %+v", got, want)
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
