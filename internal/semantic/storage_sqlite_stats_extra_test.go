package semantic

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestSQLiteStorage_MemoryStatsTracker pins the MemoryStatsTracker implementation end to
// end: batched retrievals upsert one stats row per memory, the per-memory and all-memory
// readers agree, the joined question comes from the memory table, and a status update
// preserves the retrieval count.
func TestSQLiteStorage_MemoryStatsTracker(t *testing.T) {
	storage := memoryFilterFixture(t)
	ctx := context.Background()

	// Storage must satisfy the optional tracker interface.
	var tracker MemoryStatsTracker = storage

	if err := tracker.TrackMemoryRetrievalBatch(ctx, []MemoryRetrieval{
		{MemoryID: "m1", Score: 0.6},
		{MemoryID: "m1", Score: 0.8},
		{MemoryID: "m2", Score: 0.3},
	}, "batch query"); err != nil {
		t.Fatalf("TrackMemoryRetrievalBatch() error = %v", err)
	}

	stats, err := tracker.GetMemoryStats(ctx, "m1")
	if err != nil || stats == nil {
		t.Fatalf("GetMemoryStats(m1) = %v, %v", stats, err)
	}
	if stats.RetrievalCount != 2 {
		t.Errorf("m1 RetrievalCount = %d, want 2", stats.RetrievalCount)
	}
	if stats.AvgScore < 0.69 || stats.AvgScore > 0.71 {
		t.Errorf("m1 AvgScore = %v, want ~0.70", stats.AvgScore)
	}

	all, err := tracker.GetAllMemoryStats(ctx)
	if err != nil {
		t.Fatalf("GetAllMemoryStats() error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("GetAllMemoryStats() returned %d rows, want 2", len(all))
	}
	if all[0].MemoryID != "m1" {
		t.Errorf("first row = %q, want m1 (ordered by retrieval count)", all[0].MemoryID)
	}
	if all[0].Question != "q1" {
		t.Errorf("Question = %q, want the joined memory question q1", all[0].Question)
	}
	if all[0].CreatedAt == "" {
		t.Error("CreatedAt should come from the joined memory row")
	}

	history, err := tracker.GetMemoryRetrievalHistory(ctx, "m1", 10)
	if err != nil {
		t.Fatalf("GetMemoryRetrievalHistory() error = %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history has %d entries, want 2", len(history))
	}
	if history[0].Query != "batch query" {
		t.Errorf("history query = %q, want \"batch query\"", history[0].Query)
	}

	if err := tracker.UpdateMemoryStatsStatus(ctx, "m1", "archived"); err != nil {
		t.Fatalf("UpdateMemoryStatsStatus() error = %v", err)
	}
	stats, err = tracker.GetMemoryStats(ctx, "m1")
	if err != nil || stats == nil {
		t.Fatalf("GetMemoryStats(m1) = %v, %v", stats, err)
	}
	if stats.Status != "archived" {
		t.Errorf("Status = %q, want \"archived\"", stats.Status)
	}
	if stats.RetrievalCount != 2 {
		t.Errorf("RetrievalCount = %d after a status update, want 2 preserved", stats.RetrievalCount)
	}

	// A single tracked retrieval adds to the same row.
	if err := tracker.TrackMemoryRetrieval(ctx, "m1", "single query", 1.0); err != nil {
		t.Fatalf("TrackMemoryRetrieval() error = %v", err)
	}
	stats, err = tracker.GetMemoryStats(ctx, "m1")
	if err != nil || stats == nil {
		t.Fatalf("GetMemoryStats(m1) = %v, %v", stats, err)
	}
	if stats.RetrievalCount != 3 {
		t.Errorf("RetrievalCount = %d after a third retrieval, want 3", stats.RetrievalCount)
	}

	// An empty batch is a no-op.
	if err := tracker.TrackMemoryRetrievalBatch(ctx, nil, "q"); err != nil {
		t.Errorf("TrackMemoryRetrievalBatch(nil) error = %v, want nil", err)
	}
}

// TestSQLiteStorage_PruneMemoryRetrievalLog pins that pruning deletes only backdated log
// rows and reports the count.
func TestSQLiteStorage_PruneMemoryRetrievalLog_BackdatedRows(t *testing.T) {
	storage := memoryFilterFixture(t)
	ctx := context.Background()

	if err := storage.TrackMemoryRetrieval(ctx, "m1", "recent", 0.5); err != nil {
		t.Fatalf("TrackMemoryRetrieval() error = %v", err)
	}
	if _, err := storage.db.Exec(`
		INSERT INTO retrieval_log (memory_id, query, score, timestamp)
		VALUES (?, ?, ?, datetime('now', '-60 days'))
	`, "m1", "stale", 0.1); err != nil {
		t.Fatalf("failed to insert a stale log row: %v", err)
	}

	removed, err := storage.PruneMemoryRetrievalLog(ctx, 30)
	if err != nil {
		t.Fatalf("PruneMemoryRetrievalLog() error = %v", err)
	}
	if removed != 1 {
		t.Errorf("PruneMemoryRetrievalLog(30) removed %d rows, want 1", removed)
	}

	history, err := storage.GetMemoryRetrievalHistory(ctx, "m1", 100)
	if err != nil {
		t.Fatalf("GetMemoryRetrievalHistory() error = %v", err)
	}
	if len(history) != 1 || history[0].Query != "recent" {
		t.Errorf("history after prune = %+v, want only the recent entry", history)
	}
}

// TestSQLiteStorage_GetMemory pins the single-entry reader: a stored entry round-trips
// with its multi-valued columns, and a missing id is reported as ErrMemoryNotFound.
func TestSQLiteStorage_GetMemory(t *testing.T) {
	storage := memoryFilterFixture(t)
	ctx := context.Background()

	entry, err := storage.GetMemory(ctx, "m4")
	if err != nil {
		t.Fatalf("GetMemory() error = %v", err)
	}
	if entry.Question != "q4" || entry.Source != "sprint" {
		t.Errorf("entry = %+v, want q4 from the sprint source", entry)
	}
	if len(entry.Sprints) != 2 || entry.Sprints[1] != "7.1" {
		t.Errorf("Sprints = %v, want [7.0 7.1]", entry.Sprints)
	}
	if len(entry.Files) != 2 || entry.Files[0] != "a.go" {
		t.Errorf("Files = %v, want [a.go b.go]", entry.Files)
	}

	if _, err := storage.GetMemory(ctx, "does-not-exist"); !errors.Is(err, ErrMemoryNotFound) {
		t.Errorf("GetMemory(unknown) error = %v, want ErrMemoryNotFound", err)
	}
}

// TestDecodeEmbedding pins both decode paths: the packed little-endian float32 form and
// the legacy JSON fallback used when the byte length is not a multiple of four.
func TestDecodeEmbedding(t *testing.T) {
	packed, err := encodeEmbedding([]float32{1.5, -2.25, 0})
	if err != nil {
		t.Fatalf("encodeEmbedding() error = %v", err)
	}
	if len(packed) != 12 {
		t.Fatalf("encodeEmbedding() produced %d bytes, want 12", len(packed))
	}
	got, err := decodeEmbedding(packed)
	if err != nil {
		t.Fatalf("decodeEmbedding() error = %v", err)
	}
	if len(got) != 3 || got[0] != 1.5 || got[1] != -2.25 || got[2] != 0 {
		t.Errorf("decodeEmbedding() = %v, want [1.5 -2.25 0]", got)
	}

	// A five-byte JSON payload is not a multiple of four, so the legacy path runs.
	legacy, err := json.Marshal([]float32{0.5})
	if err != nil {
		t.Fatalf("marshal error = %v", err)
	}
	if len(legacy)%4 == 0 {
		t.Fatalf("test fixture %q happens to be 4-byte aligned; the legacy path would not run", legacy)
	}
	got, err = decodeEmbedding(legacy)
	if err != nil {
		t.Fatalf("decodeEmbedding(legacy JSON) error = %v", err)
	}
	if len(got) != 1 || got[0] != 0.5 {
		t.Errorf("decodeEmbedding(legacy JSON) = %v, want [0.5]", got)
	}

	if _, err := decodeEmbedding([]byte("xyz")); err == nil {
		t.Error("decodeEmbedding() on unparseable non-aligned bytes should fail")
	}
}

// legacyChunksDB creates a database whose chunks table predates the file_mtime column.
func legacyChunksDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "legacy.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open legacy db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`
		CREATE TABLE chunks (
			id TEXT PRIMARY KEY,
			file_path TEXT NOT NULL,
			type TEXT NOT NULL,
			name TEXT NOT NULL,
			signature TEXT,
			content TEXT,
			start_line INTEGER,
			end_line INTEGER,
			language TEXT,
			embedding BLOB
		)
	`); err != nil {
		t.Fatalf("failed to create the legacy chunks table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO chunks (id, file_path, type, name, content) VALUES ('old', 'a.go', 'function', 'Old', 'body')`); err != nil {
		t.Fatalf("failed to seed the legacy row: %v", err)
	}
	return db, dbPath
}

// TestSQLiteStorage_MigrateMtimeColumn pins the backward-compatibility migration itself:
// run against a pre-file_mtime chunks table it adds the column and its index, leaves
// existing rows intact, and is idempotent on a second run.
func TestSQLiteStorage_MigrateMtimeColumn(t *testing.T) {
	db, _ := legacyChunksDB(t)
	storage := &SQLiteStorage{db: db, embeddingDim: 4}

	if err := storage.migrateMtimeColumn(); err != nil {
		t.Fatalf("migrateMtimeColumn() error = %v", err)
	}

	var cols int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('chunks') WHERE name = 'file_mtime'`).Scan(&cols); err != nil {
		t.Fatalf("pragma query failed: %v", err)
	}
	if cols != 1 {
		t.Errorf("file_mtime column count = %d, want 1 after the migration", cols)
	}

	var idx int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_chunks_file_mtime'`).Scan(&idx); err != nil {
		t.Fatalf("index query failed: %v", err)
	}
	if idx != 1 {
		t.Errorf("idx_chunks_file_mtime count = %d, want 1", idx)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM chunks WHERE id = 'old'`).Scan(&name); err != nil {
		t.Fatalf("the pre-existing row did not survive the migration: %v", err)
	}
	if name != "Old" {
		t.Errorf("surviving row name = %q, want Old", name)
	}

	// Running again must be a no-op, not a duplicate-column error.
	if err := storage.migrateMtimeColumn(); err != nil {
		t.Errorf("migrateMtimeColumn() second run error = %v, want nil (idempotent)", err)
	}
}

// TestSQLiteStorage_OpenLegacyDatabase documents a defect: initSchema executes its whole
// schema block - including `CREATE INDEX ... ON chunks(file_mtime)` - before calling
// migrateMtimeColumn, so opening a database written before that column existed fails and
// the migration never runs. LexicalIndex.initSchema orders the two the other way round
// and carries a comment saying the migration must come first.
func TestSQLiteStorage_OpenLegacyDatabase(t *testing.T) {
	db, dbPath := legacyChunksDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("failed to close legacy db: %v", err)
	}

	_, err := NewSQLiteStorage(dbPath, 4)
	if err == nil {
		t.Fatal("NewSQLiteStorage() on a pre-file_mtime database unexpectedly succeeded; the migration ordering may have been fixed")
	}
	if !strings.Contains(err.Error(), "file_mtime") {
		t.Errorf("error = %v, want it to name the missing file_mtime column", err)
	}
}

// TestSQLiteStorage_Update pins in-place chunk replacement: the stored content and the
// embedding both change, and updating an unknown id reports ErrNotFound.
func TestSQLiteStorage_Update(t *testing.T) {
	storage, err := NewSQLiteStorage(filepath.Join(t.TempDir(), "update.db"), 4)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer storage.Close()

	ctx := context.Background()
	chunk := Chunk{ID: "u1", FilePath: "a.go", Name: "A", Type: ChunkFunction, Content: "original"}
	if err := storage.Create(ctx, chunk, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	chunk.Content = "rewritten"
	chunk.Name = "ARenamed"
	if err := storage.Update(ctx, chunk, []float32{0, 1, 0, 0}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	got, err := storage.Read(ctx, "u1")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got.Content != "rewritten" || got.Name != "ARenamed" {
		t.Errorf("chunk after Update = %+v, want the rewritten content and name", got)
	}

	embs, err := storage.ReadEmbeddings(ctx, []string{"u1"})
	if err != nil {
		t.Fatalf("ReadEmbeddings() error = %v", err)
	}
	if v := embs["u1"]; len(v) != 4 || v[1] != 1 {
		t.Errorf("embedding after Update = %v, want the new [0 1 0 0]", v)
	}

	missing := Chunk{ID: "nope", FilePath: "a.go", Name: "X", Type: ChunkFunction, Content: "x"}
	if err := storage.Update(ctx, missing, []float32{1, 0, 0, 0}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update(unknown id) error = %v, want ErrNotFound", err)
	}
}
