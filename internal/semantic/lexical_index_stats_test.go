package semantic

import (
	"strings"
	"testing"
)

// newStatsIndex builds an in-memory LexicalIndex for the retrieval-stats tests.
func newStatsIndex(t *testing.T) *LexicalIndex {
	t.Helper()
	idx, err := NewLexicalIndex(":memory:", 4)
	if err != nil {
		t.Fatalf("failed to create lexical index: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

// TestLexicalIndex_TrackRetrieval_IncrementsCount pins that TrackRetrieval upserts:
// the first call creates the row with count 1 and status "active", the second
// increments rather than inserting a duplicate.
func TestLexicalIndex_TrackRetrieval_IncrementsCount(t *testing.T) {
	idx := newStatsIndex(t)

	if err := idx.TrackRetrieval("mem-1", "first query", 0.5); err != nil {
		t.Fatalf("TrackRetrieval() error = %v", err)
	}

	stats, err := idx.GetMemoryStats("mem-1")
	if err != nil {
		t.Fatalf("GetMemoryStats() error = %v", err)
	}
	if stats == nil {
		t.Fatal("GetMemoryStats() returned nil after a tracked retrieval")
	}
	if stats.RetrievalCount != 1 {
		t.Errorf("RetrievalCount = %d, want 1", stats.RetrievalCount)
	}
	if stats.Status != "active" {
		t.Errorf("Status = %q, want \"active\"", stats.Status)
	}
	if stats.LastRetrieved == "" {
		t.Error("LastRetrieved should be set on first track")
	}

	if err := idx.TrackRetrieval("mem-1", "second query", 0.9); err != nil {
		t.Fatalf("TrackRetrieval() second call error = %v", err)
	}
	stats, err = idx.GetMemoryStats("mem-1")
	if err != nil {
		t.Fatalf("GetMemoryStats() error = %v", err)
	}
	if stats.RetrievalCount != 2 {
		t.Errorf("RetrievalCount = %d after two tracks, want 2", stats.RetrievalCount)
	}
	// AvgScore is averaged over the retrieval_log rows: (0.5 + 0.9) / 2.
	if stats.AvgScore < 0.69 || stats.AvgScore > 0.71 {
		t.Errorf("AvgScore = %v, want ~0.70 (mean of 0.5 and 0.9)", stats.AvgScore)
	}
}

// TestLexicalIndex_GetMemoryStats_UnknownID pins that an untracked id is reported as
// (nil, nil) rather than as an error, which is what callers branch on.
func TestLexicalIndex_GetMemoryStats_UnknownID(t *testing.T) {
	idx := newStatsIndex(t)

	stats, err := idx.GetMemoryStats("never-seen")
	if err != nil {
		t.Fatalf("GetMemoryStats() on unknown id error = %v, want nil", err)
	}
	if stats != nil {
		t.Errorf("GetMemoryStats() on unknown id = %+v, want nil", stats)
	}
}

// TestLexicalIndex_TrackRetrievalBatch pins that a batch writes one stats row per
// distinct memory id and one log row per retrieval, in a single transaction.
func TestLexicalIndex_TrackRetrievalBatch(t *testing.T) {
	idx := newStatsIndex(t)

	batch := []struct {
		MemoryID string
		Score    float32
	}{
		{MemoryID: "mem-a", Score: 0.9},
		{MemoryID: "mem-b", Score: 0.4},
		{MemoryID: "mem-a", Score: 0.7},
	}

	if err := idx.TrackRetrievalBatch(batch, "batch query"); err != nil {
		t.Fatalf("TrackRetrievalBatch() error = %v", err)
	}

	statsA, err := idx.GetMemoryStats("mem-a")
	if err != nil || statsA == nil {
		t.Fatalf("GetMemoryStats(mem-a) = %v, %v", statsA, err)
	}
	if statsA.RetrievalCount != 2 {
		t.Errorf("mem-a RetrievalCount = %d, want 2 (appears twice in the batch)", statsA.RetrievalCount)
	}

	statsB, err := idx.GetMemoryStats("mem-b")
	if err != nil || statsB == nil {
		t.Fatalf("GetMemoryStats(mem-b) = %v, %v", statsB, err)
	}
	if statsB.RetrievalCount != 1 {
		t.Errorf("mem-b RetrievalCount = %d, want 1", statsB.RetrievalCount)
	}

	history, err := idx.GetRetrievalHistory("mem-a", 10)
	if err != nil {
		t.Fatalf("GetRetrievalHistory() error = %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("GetRetrievalHistory(mem-a) returned %d entries, want 2", len(history))
	}
	for _, h := range history {
		if h.Query != "batch query" {
			t.Errorf("log entry query = %q, want \"batch query\"", h.Query)
		}
		if h.MemoryID != "mem-a" {
			t.Errorf("log entry memory id = %q, want mem-a", h.MemoryID)
		}
		if h.Timestamp == "" {
			t.Error("log entry timestamp should be populated by the column default")
		}
	}
}

// TestLexicalIndex_TrackRetrievalBatch_Empty pins the early return: an empty batch is a
// no-op that writes nothing, not an error and not an empty transaction.
func TestLexicalIndex_TrackRetrievalBatch_Empty(t *testing.T) {
	idx := newStatsIndex(t)

	empty := []struct {
		MemoryID string
		Score    float32
	}{}
	if err := idx.TrackRetrievalBatch(empty, "q"); err != nil {
		t.Fatalf("TrackRetrievalBatch(empty) error = %v, want nil", err)
	}

	var count int
	if err := idx.db.QueryRow(`SELECT COUNT(*) FROM retrieval_log`).Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("retrieval_log has %d rows after an empty batch, want 0", count)
	}
}

// TestLexicalIndex_GetRetrievalHistory_Limit pins that the limit caps the returned rows
// and that a non-positive limit falls back to the 100-row default instead of LIMIT 0.
func TestLexicalIndex_GetRetrievalHistory_Limit(t *testing.T) {
	idx := newStatsIndex(t)

	for i := 0; i < 5; i++ {
		if err := idx.TrackRetrieval("mem-x", "q", float32(i)/10); err != nil {
			t.Fatalf("TrackRetrieval() error = %v", err)
		}
	}

	limited, err := idx.GetRetrievalHistory("mem-x", 2)
	if err != nil {
		t.Fatalf("GetRetrievalHistory() error = %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("GetRetrievalHistory(limit=2) returned %d entries, want 2", len(limited))
	}

	all, err := idx.GetRetrievalHistory("mem-x", 0)
	if err != nil {
		t.Fatalf("GetRetrievalHistory(0) error = %v", err)
	}
	if len(all) != 5 {
		t.Errorf("GetRetrievalHistory(limit=0) returned %d entries, want 5 (default limit applies)", len(all))
	}
}

// TestLexicalIndex_PruneRetrievalLog pins that pruning deletes only rows older than the
// cutoff and reports how many it removed. The stats row must survive the prune.
func TestLexicalIndex_PruneRetrievalLog(t *testing.T) {
	idx := newStatsIndex(t)

	if err := idx.TrackRetrieval("mem-p", "recent", 0.5); err != nil {
		t.Fatalf("TrackRetrieval() error = %v", err)
	}
	// Backdate two extra log rows past the 7-day cutoff.
	for i := 0; i < 2; i++ {
		if _, err := idx.db.Exec(`
			INSERT INTO retrieval_log (memory_id, query, score, timestamp)
			VALUES (?, ?, ?, datetime('now', '-30 days'))
		`, "mem-p", "stale", 0.1); err != nil {
			t.Fatalf("failed to insert stale log row: %v", err)
		}
	}

	removed, err := idx.PruneRetrievalLog(7)
	if err != nil {
		t.Fatalf("PruneRetrievalLog() error = %v", err)
	}
	if removed != 2 {
		t.Errorf("PruneRetrievalLog(7) removed %d rows, want 2", removed)
	}

	history, err := idx.GetRetrievalHistory("mem-p", 100)
	if err != nil {
		t.Fatalf("GetRetrievalHistory() error = %v", err)
	}
	if len(history) != 1 || history[0].Query != "recent" {
		t.Errorf("after prune history = %+v, want only the recent entry", history)
	}

	stats, err := idx.GetMemoryStats("mem-p")
	if err != nil || stats == nil {
		t.Fatalf("stats row should survive a prune, got %v, %v", stats, err)
	}
}

// TestLexicalIndex_UpdateMemoryStatus pins the upsert: a status can be set for a memory
// that has never been retrieved, and a later call overwrites it without losing the count.
func TestLexicalIndex_UpdateMemoryStatus(t *testing.T) {
	idx := newStatsIndex(t)

	if err := idx.UpdateMemoryStatus("mem-s", "archived"); err != nil {
		t.Fatalf("UpdateMemoryStatus() error = %v", err)
	}
	stats, err := idx.GetMemoryStats("mem-s")
	if err != nil || stats == nil {
		t.Fatalf("GetMemoryStats() = %v, %v", stats, err)
	}
	if stats.Status != "archived" {
		t.Errorf("Status = %q, want \"archived\"", stats.Status)
	}
	if stats.RetrievalCount != 0 {
		t.Errorf("RetrievalCount = %d for a never-retrieved memory, want 0", stats.RetrievalCount)
	}

	// A retrieval bumps the count; a second status change must not reset it.
	if err := idx.TrackRetrieval("mem-s", "q", 0.5); err != nil {
		t.Fatalf("TrackRetrieval() error = %v", err)
	}
	if err := idx.UpdateMemoryStatus("mem-s", "promoted"); err != nil {
		t.Fatalf("UpdateMemoryStatus() second call error = %v", err)
	}
	stats, err = idx.GetMemoryStats("mem-s")
	if err != nil || stats == nil {
		t.Fatalf("GetMemoryStats() = %v, %v", stats, err)
	}
	if stats.Status != "promoted" {
		t.Errorf("Status = %q, want \"promoted\"", stats.Status)
	}
	if stats.RetrievalCount != 1 {
		t.Errorf("RetrievalCount = %d after a status change, want 1 (count must be preserved)", stats.RetrievalCount)
	}
}

// TestLexicalIndex_StatsAfterClose pins that every stats entry point refuses to run on a
// closed index instead of hitting a closed *sql.DB.
func TestLexicalIndex_StatsAfterClose(t *testing.T) {
	idx, err := NewLexicalIndex(":memory:", 4)
	if err != nil {
		t.Fatalf("failed to create lexical index: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	checks := []struct {
		name string
		err  error
	}{
		{"TrackRetrieval", idx.TrackRetrieval("m", "q", 0.1)},
		{"UpdateMemoryStatus", idx.UpdateMemoryStatus("m", "active")},
	}
	for _, c := range checks {
		if c.name == "" {
			continue
		}
		if c.err == nil || !strings.Contains(c.err.Error(), "closed") {
			t.Errorf("%s on closed index error = %v, want an \"index is closed\" error", c.name, c.err)
		}
	}

	batch := []struct {
		MemoryID string
		Score    float32
	}{{MemoryID: "m", Score: 0.1}}
	if err := idx.TrackRetrievalBatch(batch, "q"); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("TrackRetrievalBatch on closed index error = %v, want a closed error", err)
	}
	if _, err := idx.GetMemoryStats("m"); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("GetMemoryStats on closed index error = %v, want a closed error", err)
	}
	if _, err := idx.GetAllMemoryStats(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("GetAllMemoryStats on closed index error = %v, want a closed error", err)
	}
	if _, err := idx.GetRetrievalHistory("m", 5); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("GetRetrievalHistory on closed index error = %v, want a closed error", err)
	}
	if _, err := idx.PruneRetrievalLog(1); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("PruneRetrievalLog on closed index error = %v, want a closed error", err)
	}
}

// TestLexicalIndex_GetAllMemoryStats_JoinsMemoryTable pins that GetAllMemoryStats joins
// the memory table for the question/created_at columns, orders by retrieval count
// descending, and averages the logged scores.
//
// The join means a standalone LexicalIndex (which never creates a `memory` table) cannot
// serve this query, so the table is created here the way SQLiteStorage creates it.
func TestLexicalIndex_GetAllMemoryStats_JoinsMemoryTable(t *testing.T) {
	idx := newStatsIndex(t)

	if _, err := idx.db.Exec(`
		CREATE TABLE memory (
			id TEXT PRIMARY KEY,
			question TEXT,
			created_at TEXT
		)
	`); err != nil {
		t.Fatalf("failed to create memory table: %v", err)
	}
	if _, err := idx.db.Exec(`INSERT INTO memory (id, question, created_at) VALUES (?, ?, ?)`,
		"mem-hot", "why is it hot?", "2026-01-01"); err != nil {
		t.Fatalf("failed to seed memory row: %v", err)
	}

	// mem-hot is retrieved twice, mem-cold once, so mem-hot must sort first.
	if err := idx.TrackRetrieval("mem-cold", "q", 0.2); err != nil {
		t.Fatalf("TrackRetrieval() error = %v", err)
	}
	for _, s := range []float32{0.6, 0.8} {
		if err := idx.TrackRetrieval("mem-hot", "q", s); err != nil {
			t.Fatalf("TrackRetrieval() error = %v", err)
		}
	}

	all, err := idx.GetAllMemoryStats()
	if err != nil {
		t.Fatalf("GetAllMemoryStats() error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("GetAllMemoryStats() returned %d rows, want 2", len(all))
	}
	if all[0].MemoryID != "mem-hot" {
		t.Errorf("first row = %q, want mem-hot (ordered by retrieval_count DESC)", all[0].MemoryID)
	}
	if all[0].RetrievalCount != 2 {
		t.Errorf("mem-hot RetrievalCount = %d, want 2", all[0].RetrievalCount)
	}
	if all[0].AvgScore < 0.69 || all[0].AvgScore > 0.71 {
		t.Errorf("mem-hot AvgScore = %v, want ~0.70", all[0].AvgScore)
	}
	if all[0].Question != "why is it hot?" {
		t.Errorf("Question = %q, want the joined memory question", all[0].Question)
	}
	if all[0].CreatedAt != "2026-01-01" {
		t.Errorf("CreatedAt = %q, want the joined memory created_at", all[0].CreatedAt)
	}
	// mem-cold has no matching memory row, so the LEFT JOIN leaves those fields empty.
	if all[1].MemoryID != "mem-cold" || all[1].Question != "" {
		t.Errorf("second row = %+v, want mem-cold with an empty question", all[1])
	}
}

// TestLexicalIndex_GetAllMemoryStats_NoMemoryTable documents that a standalone
// LexicalIndex cannot answer GetAllMemoryStats: initSchema never creates the `memory`
// table the query LEFT JOINs against, so the call fails rather than returning the
// stats rows that do exist.
func TestLexicalIndex_GetAllMemoryStats_NoMemoryTable(t *testing.T) {
	idx := newStatsIndex(t)

	if err := idx.TrackRetrieval("mem-1", "q", 0.5); err != nil {
		t.Fatalf("TrackRetrieval() error = %v", err)
	}

	_, err := idx.GetAllMemoryStats()
	if err == nil {
		t.Fatal("GetAllMemoryStats() on a standalone index returned no error; the memory table join must fail")
	}
	if !strings.Contains(err.Error(), "memory") {
		t.Errorf("error = %v, want it to name the missing memory table", err)
	}
}
