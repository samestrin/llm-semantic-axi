package semantic

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// ftsFixture creates a file-backed SQLiteStorage holding two chunks.
func ftsFixture(t *testing.T) *SQLiteStorage {
	t.Helper()
	storage, err := NewSQLiteStorage(filepath.Join(t.TempDir(), "fts.db"), 4)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	ctx := context.Background()
	chunks := []Chunk{
		{ID: "a", FilePath: "widget.go", Name: "Widget", Type: ChunkFunction, Content: "func Widget() { spinner() }"},
		{ID: "b", FilePath: "gadget.go", Name: "Gadget", Type: ChunkFunction, Content: "func Gadget() { lever() }"},
	}
	for _, c := range chunks {
		if err := storage.Create(ctx, c, []float32{1, 0, 0, 0}); err != nil {
			t.Fatalf("failed to create chunk %s: %v", c.ID, err)
		}
	}
	return storage
}

// TestRebuildFTSIndex_RestoresLostRows pins the repair path: after the FTS shadow table
// is emptied behind the triggers' back, a search finds nothing until RebuildFTSIndex
// repopulates it from the chunks table.
func TestRebuildFTSIndex_RestoresLostRows(t *testing.T) {
	storage := ftsFixture(t)
	ctx := context.Background()

	if _, err := storage.db.Exec(`DELETE FROM chunks_fts`); err != nil {
		t.Fatalf("failed to clear the FTS table: %v", err)
	}
	before, err := storage.LexicalSearch(ctx, "spinner", LexicalSearchOptions{TopK: 5})
	if err != nil {
		t.Fatalf("LexicalSearch() error = %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("expected an empty FTS index to return no results, got %d", len(before))
	}

	if err := storage.RebuildFTSIndex(); err != nil {
		t.Fatalf("RebuildFTSIndex() error = %v", err)
	}

	after, err := storage.LexicalSearch(ctx, "spinner", LexicalSearchOptions{TopK: 5})
	if err != nil {
		t.Fatalf("LexicalSearch() after rebuild error = %v", err)
	}
	if len(after) != 1 || after[0].Chunk.ID != "a" {
		t.Errorf("after rebuild results = %v, want just chunk a", ids(after))
	}

	var count int
	if err := storage.db.QueryRow(`SELECT COUNT(*) FROM chunks_fts`).Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 2 {
		t.Errorf("chunks_fts has %d rows after rebuild, want 2 (one per chunk)", count)
	}
}

// TestRebuildFTSIndex_ClosedStorage pins that the rebuild refuses to touch a closed
// database handle.
func TestRebuildFTSIndex_ClosedStorage(t *testing.T) {
	storage, err := NewSQLiteStorage(filepath.Join(t.TempDir(), "closed.db"), 4)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if err := storage.RebuildFTSIndex(); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("RebuildFTSIndex() on closed storage = %v, want ErrStorageClosed", err)
	}
	if err := storage.RebuildMemoryFTSIndex(); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("RebuildMemoryFTSIndex() on closed storage = %v, want ErrStorageClosed", err)
	}
	if err := storage.FTSIntegrityCheck(); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("FTSIntegrityCheck() on closed storage = %v, want ErrStorageClosed", err)
	}
}

// TestFTSIntegrityCheck_HealthyIndex pins that a freshly built index passes the
// integrity check.
func TestFTSIntegrityCheck_HealthyIndex(t *testing.T) {
	storage := ftsFixture(t)

	if err := storage.FTSIntegrityCheck(); err != nil {
		t.Errorf("FTSIntegrityCheck() on a healthy index = %v, want nil", err)
	}
}

// TestSafeLexicalSearch_EscapesOperators pins the difference between the raw and the safe
// entry points: a query containing an FTS5 operator character is a syntax error through
// LexicalSearch but is treated as literal text through SafeLexicalSearch.
func TestSafeLexicalSearch_EscapesOperators(t *testing.T) {
	storage := ftsFixture(t)
	ctx := context.Background()

	if _, err := storage.LexicalSearch(ctx, `spinner"`, LexicalSearchOptions{TopK: 5}); err == nil {
		t.Fatal("LexicalSearch() with an unbalanced quote should fail")
	}

	results, err := storage.SafeLexicalSearch(ctx, `spinner"`, LexicalSearchOptions{TopK: 5})
	if err != nil {
		t.Fatalf("SafeLexicalSearch() should escape the quote, got %v", err)
	}
	if len(results) != 1 || results[0].Chunk.ID != "a" {
		t.Errorf("SafeLexicalSearch() = %v, want chunk a", ids(results))
	}
}

// TestSafeLexicalSearch_EmptyQuery pins the short-circuit: an empty query returns an
// empty (non-nil) slice rather than running a MATCH against the empty string.
func TestSafeLexicalSearch_EmptyQuery(t *testing.T) {
	storage := ftsFixture(t)

	results, err := storage.SafeLexicalSearch(context.Background(), "", LexicalSearchOptions{})
	if err != nil {
		t.Fatalf("SafeLexicalSearch(\"\") error = %v", err)
	}
	if results == nil {
		t.Error("SafeLexicalSearch(\"\") returned nil, want an empty slice")
	}
	if len(results) != 0 {
		t.Errorf("SafeLexicalSearch(\"\") returned %d results, want 0", len(results))
	}
}

// TestIsFTS5SyntaxError pins the classifier that decides whether a query failure should
// be reported as a user syntax error.
func TestIsFTS5SyntaxError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"fts5 syntax error", errors.New("SQL logic error: fts5: syntax error near \"*\""), true},
		{"malformed match", errors.New("malformed MATCH expression"), true},
		{"unrelated", errors.New("disk I/O error"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isFTS5SyntaxError(tt.err); got != tt.want {
				t.Errorf("isFTS5SyntaxError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}

	if !contains("abcdef", "cde") {
		t.Error("contains() should report a present substring")
	}
	if contains("abcdef", "xyz") {
		t.Error("contains() should reject an absent substring")
	}
}

// TestFTSTableExists pins the migration probe: true only once the chunks_fts virtual
// table has been created.
func TestFTSTableExists(t *testing.T) {
	storage := ftsFixture(t)

	if !ftsTableExists(storage.db) {
		t.Error("ftsTableExists() = false on an initialised storage, want true")
	}

	if _, err := storage.db.Exec(`DROP TABLE chunks_fts`); err != nil {
		t.Fatalf("failed to drop chunks_fts: %v", err)
	}
	if ftsTableExists(storage.db) {
		t.Error("ftsTableExists() = true after the table was dropped, want false")
	}
}

// TestMatchesTags pins the any-of tag matcher used to post-filter memory FTS results.
func TestMatchesTags(t *testing.T) {
	tests := []struct {
		name       string
		entryTags  []string
		filterTags []string
		want       bool
	}{
		{"one overlap", []string{"go", "sqlite"}, []string{"sqlite"}, true},
		{"no overlap", []string{"go"}, []string{"rust"}, false},
		{"empty entry tags", nil, []string{"go"}, false},
		{"empty filter tags", []string{"go"}, nil, false},
		{"case sensitive", []string{"Go"}, []string{"go"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesTags(tt.entryTags, tt.filterTags); got != tt.want {
				t.Errorf("matchesTags(%v, %v) = %v, want %v", tt.entryTags, tt.filterTags, got, tt.want)
			}
		})
	}
}

// memoryFTSFixture stores three memory entries with distinct sources, statuses and tags.
func memoryFTSFixture(t *testing.T) *SQLiteStorage {
	t.Helper()
	storage, err := NewSQLiteStorage(filepath.Join(t.TempDir(), "memfts.db"), 4)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	ctx := context.Background()
	entries := []struct {
		entry MemoryEntry
		emb   []float32
	}{
		{MemoryEntry{
			ID: "m1", Question: "how does the watchdog rearm?", Answer: "it restarts the timer",
			Tags: []string{"streaming"}, Source: "sprint", Status: MemoryStatusPending,
			CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
		}, []float32{1, 0, 0, 0}},
		{MemoryEntry{
			ID: "m2", Question: "why does the watchdog cancel?", Answer: "it used to, then it did not",
			Tags: []string{"parsing"}, Source: "manual", Status: MemoryStatusPromoted,
			CreatedAt: "2026-01-02T00:00:00Z", UpdatedAt: "2026-01-02T00:00:00Z",
		}, []float32{0, 1, 0, 0}},
		{MemoryEntry{
			ID: "m3", Question: "what is a lever?", Answer: "a simple machine",
			Tags: []string{"misc"}, Source: "manual", Status: MemoryStatusPending,
			CreatedAt: "2026-01-03T00:00:00Z", UpdatedAt: "2026-01-03T00:00:00Z",
		}, []float32{0, 0, 1, 0}},
	}
	for _, e := range entries {
		if err := storage.StoreMemory(ctx, e.entry, e.emb); err != nil {
			t.Fatalf("failed to store memory %s: %v", e.entry.ID, err)
		}
	}
	return storage
}

// TestLexicalSearchMemory_Filters pins the memory FTS query: the MATCH narrows to the
// question/answer text and the status, source and tag filters narrow it further.
func TestLexicalSearchMemory_Filters(t *testing.T) {
	storage := memoryFTSFixture(t)
	ctx := context.Background()

	all, err := storage.LexicalSearchMemory(ctx, "watchdog", MemoryLexicalSearchOptions{TopK: 10})
	if err != nil {
		t.Fatalf("LexicalSearchMemory() error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("\"watchdog\" matched %d entries, want 2", len(all))
	}

	byStatus, err := storage.LexicalSearchMemory(ctx, "watchdog", MemoryLexicalSearchOptions{
		TopK: 10, Status: MemoryStatusPromoted,
	})
	if err != nil {
		t.Fatalf("LexicalSearchMemory() error = %v", err)
	}
	if len(byStatus) != 1 || byStatus[0].Entry.ID != "m2" {
		t.Errorf("status filter returned %d entries, want just m2", len(byStatus))
	}

	bySource, err := storage.LexicalSearchMemory(ctx, "watchdog", MemoryLexicalSearchOptions{
		TopK: 10, Source: "sprint",
	})
	if err != nil {
		t.Fatalf("LexicalSearchMemory() error = %v", err)
	}
	if len(bySource) != 1 || bySource[0].Entry.ID != "m1" {
		t.Errorf("source filter returned %d entries, want just m1", len(bySource))
	}
	if bySource[0].Entry.Source != "sprint" {
		t.Errorf("Source = %q, want \"sprint\"", bySource[0].Entry.Source)
	}
	if len(bySource[0].Entry.Tags) != 1 || bySource[0].Entry.Tags[0] != "streaming" {
		t.Errorf("Tags = %v, want [streaming] parsed from the CSV column", bySource[0].Entry.Tags)
	}

	byTag, err := storage.LexicalSearchMemory(ctx, "watchdog", MemoryLexicalSearchOptions{
		TopK: 10, Tags: []string{"parsing"},
	})
	if err != nil {
		t.Fatalf("LexicalSearchMemory() error = %v", err)
	}
	if len(byTag) != 1 || byTag[0].Entry.ID != "m2" {
		t.Errorf("tag filter returned %d entries, want just m2", len(byTag))
	}

	none, err := storage.LexicalSearchMemory(ctx, "watchdog", MemoryLexicalSearchOptions{
		TopK: 10, Tags: []string{"nonexistent"},
	})
	if err != nil {
		t.Fatalf("LexicalSearchMemory() error = %v", err)
	}
	if len(none) != 0 {
		t.Errorf("unmatched tag filter returned %d entries, want 0", len(none))
	}
}

// TestRebuildMemoryFTSIndex_RestoresLostRows pins the memory-side repair path, mirroring
// the chunks one: an emptied memory_fts table stops matching until the rebuild runs.
func TestRebuildMemoryFTSIndex_RestoresLostRows(t *testing.T) {
	storage := memoryFTSFixture(t)
	ctx := context.Background()

	if _, err := storage.db.Exec(`DELETE FROM memory_fts`); err != nil {
		t.Fatalf("failed to clear memory_fts: %v", err)
	}
	before, err := storage.LexicalSearchMemory(ctx, "watchdog", MemoryLexicalSearchOptions{TopK: 10})
	if err != nil {
		t.Fatalf("LexicalSearchMemory() error = %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("expected no matches from an empty memory FTS index, got %d", len(before))
	}

	if err := storage.RebuildMemoryFTSIndex(); err != nil {
		t.Fatalf("RebuildMemoryFTSIndex() error = %v", err)
	}

	after, err := storage.LexicalSearchMemory(ctx, "watchdog", MemoryLexicalSearchOptions{TopK: 10})
	if err != nil {
		t.Fatalf("LexicalSearchMemory() after rebuild error = %v", err)
	}
	if len(after) != 2 {
		t.Errorf("after rebuild got %d matches, want 2", len(after))
	}
}

// TestSplitCSV pins the trimming CSV splitter that parses the memory tag columns,
// including that it drops empty fields instead of producing blank tags.
func TestSplitCSV(t *testing.T) {
	got := splitCSV(" a , b ,, c ")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("splitCSV() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("splitCSV()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if empty := splitCSV(""); len(empty) != 0 {
		t.Errorf("splitCSV(\"\") = %v, want an empty slice", empty)
	}
}
