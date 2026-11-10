package semantic

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

// assertFallbackStore defends the per-file record of why a file was chunked as
// text windows: a resumed index skips the file and index-status reads the
// record, so a reason lost or never cleared misreports what was indexed.
func assertFallbackStore(t *testing.T, fs FallbackStore, storage Storage) {
	t.Helper()
	ctx := context.Background()

	for path, reason := range map[string]FallbackReason{
		"/r/parse.py":   FallbackParseFailed,
		"/r/chunker.py": FallbackChunkerFailed,
		"/r/clean.py":   FallbackNone,
	} {
		if err := fs.SetFileHashWithFallback(ctx, path, "h1", reason); err != nil {
			t.Fatalf("SetFileHashWithFallback(%s): %v", path, err)
		}
	}
	got, err := fs.ListFallbackFiles(ctx)
	if err != nil {
		t.Fatalf("ListFallbackFiles: %v", err)
	}
	want := map[string]FallbackReason{"/r/parse.py": FallbackParseFailed, "/r/chunker.py": FallbackChunkerFailed}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListFallbackFiles = %v, want %v", got, want)
	}
	if hash, err := storage.GetFileHash(ctx, "/r/parse.py"); err != nil || hash != "h1" {
		t.Errorf("GetFileHash after SetFileHashWithFallback = %q, %v; want h1", hash, err)
	}

	// A plain hash write records a file that did not fall back.
	if err := storage.SetFileHash(ctx, "/r/parse.py", "h2"); err != nil {
		t.Fatalf("SetFileHash: %v", err)
	}
	got, err = fs.ListFallbackFiles(ctx)
	if err != nil {
		t.Fatalf("ListFallbackFiles: %v", err)
	}
	if _, still := got["/r/parse.py"]; still || len(got) != 1 {
		t.Errorf("ListFallbackFiles after a clean re-hash = %v, want only chunker.py", got)
	}
}

func TestSQLiteStorage_FallbackStore(t *testing.T) {
	storage := newFileStorage(t)
	assertFallbackStore(t, storage, storage)
}

func TestQdrantStorage_FallbackStore(t *testing.T) {
	storage, _ := newFakeQdrantStorage(t, "fallback_store", 4)
	assertFallbackStore(t, storage, storage)
}

// TestSQLiteStorage_OpenAFileHashesTableWithoutFallbackReason defends opening
// an index written before the fallback reason was recorded: the column is
// added, the existing hash survives and reads as no fallback.
func TestSQLiteStorage_OpenAFileHashesTableWithoutFallbackReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE file_hashes (file_path TEXT PRIMARY KEY, content_hash TEXT NOT NULL, indexed_at DATETIME DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO file_hashes (file_path, content_hash) VALUES ('/r/old.py', 'old')`); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	db.Close()

	storage, err := NewSQLiteStorage(path, 4)
	if err != nil {
		t.Fatalf("NewSQLiteStorage on a legacy file_hashes table: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	ctx := context.Background()

	if hash, err := storage.GetFileHash(ctx, "/r/old.py"); err != nil || hash != "old" {
		t.Errorf("legacy hash = %q, %v; want it kept", hash, err)
	}
	if got, err := storage.ListFallbackFiles(ctx); err != nil || len(got) != 0 {
		t.Errorf("ListFallbackFiles on a legacy table = %v, %v; want none", got, err)
	}
	if err := storage.SetFileHashWithFallback(ctx, "/r/old.py", "new", FallbackParseFailed); err != nil {
		t.Fatalf("SetFileHashWithFallback after the migration: %v", err)
	}
}

// TestIndex_ResumeReReportsFilesThatFellBack defends a resumed `index`: it
// skips files already indexed and unchanged, and counted none of them as
// fallen back, so a run over a tree with an unparseable file reported zero
// text-window files the second time. Both index paths skip on resume.
func TestIndex_ResumeReReportsFilesThatFellBack(t *testing.T) {
	for name, opts := range map[string]IndexOptions{
		"per-file":         {},
		"cross-file batch": {EmbedBatchSize: 100},
	} {
		t.Run(name, func(t *testing.T) {
			dir, mgr := fallbackFixture(t, map[string]string{
				"good.fb":    "fine",
				"bad.fb":     "BROKEN",
				"chunker.fb": "PANIC",
			})
			ctx := context.Background()
			if _, err := mgr.Index(ctx, dir, opts); err != nil {
				t.Fatalf("Index: %v", err)
			}

			result, err := mgr.Index(ctx, dir, opts)
			if err != nil {
				t.Fatalf("resumed Index: %v", err)
			}
			if result.FilesUnchanged != 3 {
				t.Fatalf("resumed Index FilesUnchanged = %d, want 3, so it did resume", result.FilesUnchanged)
			}
			if result.FilesFallback != 2 || !containsPath(result.FallbackFiles, filepath.Join(dir, "bad.fb")) {
				t.Errorf("resumed Index FilesFallback = %d, FallbackFiles = %v; want 2 naming bad.fb", result.FilesFallback, result.FallbackFiles)
			}
			wantChunker := []string{filepath.Join(dir, "chunker.fb")}
			if !reflect.DeepEqual(result.FallbackChunkerFailedFiles, wantChunker) {
				t.Errorf("resumed Index FallbackChunkerFailedFiles = %v, want %v", result.FallbackChunkerFailedFiles, wantChunker)
			}
		})
	}
}

// TestUpdate_ClearsTheFallbackOfAFixedFile defends the record going stale the
// other way: once index-update re-chunks a file that now parses, it must no
// longer be listed as fallen back.
func TestUpdate_ClearsTheFallbackOfAFixedFile(t *testing.T) {
	dir, mgr := fallbackFixture(t, map[string]string{"bad.fb": "BROKEN"})
	ctx := context.Background()
	if _, err := mgr.Index(ctx, dir, IndexOptions{}); err != nil {
		t.Fatalf("Index: %v", err)
	}
	fs := mgr.storage.(FallbackStore)
	if got, _ := fs.ListFallbackFiles(ctx); len(got) != 1 {
		t.Fatalf("after Index ListFallbackFiles = %v, want bad.fb", got)
	}

	writeFallbackFile(t, filepath.Join(dir, "bad.fb"), "fixed")
	if _, err := mgr.Update(ctx, dir, UpdateOptions{}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got, err := fs.ListFallbackFiles(ctx); err != nil || len(got) != 0 {
		t.Errorf("after the fix ListFallbackFiles = %v, %v; want none", got, err)
	}
}
