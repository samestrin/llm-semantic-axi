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
	// The per-file read answers for one file without listing the index.
	for path, want := range map[string]FallbackReason{
		"/r/parse.py":   FallbackParseFailed,
		"/r/chunker.py": FallbackChunkerFailed,
		"/r/clean.py":   FallbackNone,
		"/r/absent.py":  FallbackNone,
	} {
		if got, err := fs.GetFallbackReason(ctx, path); err != nil || got != want {
			t.Errorf("GetFallbackReason(%s) = %q, %v; want %q", path, got, err, want)
		}
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
	if reason, err := fs.GetFallbackReason(ctx, "/r/parse.py"); err != nil || reason != FallbackNone {
		t.Errorf("GetFallbackReason after a clean re-hash = %q, %v; want none", reason, err)
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
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	storage, err := NewSQLiteStorage(path, 4)
	if err != nil {
		t.Fatalf("NewSQLiteStorage on a legacy file_hashes table: %v", err)
	}
	t.Cleanup(func() {
		if err := storage.Close(); err != nil {
			t.Errorf("close storage: %v", err)
		}
	})
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

// TestSQLiteStorage_DeleteByFilePathIsAtomic defends the chunk delete and the
// file_hashes delete sharing one transaction: when either delete fails, the
// other must be rolled back. A half-applied delete leaves the file searchable
// with no hash row (re-embedded as changed on every later scan) or unsearchable
// with a stale one. The aborting triggers and the cancelled context are the
// injected failures; a real database in t.TempDir makes them cheap.
func TestSQLiteStorage_DeleteByFilePathIsAtomic(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sabotage func(t *testing.T, s *SQLiteStorage) context.Context
	}{
		{"hash delete fails", func(t *testing.T, s *SQLiteStorage) context.Context {
			if _, err := s.db.Exec(`CREATE TRIGGER sabotage_hash_delete BEFORE DELETE ON file_hashes BEGIN SELECT RAISE(ABORT, 'sabotage'); END;`); err != nil {
				t.Fatalf("create sabotage trigger: %v", err)
			}
			return context.Background()
		}},
		{"chunk delete fails", func(t *testing.T, s *SQLiteStorage) context.Context {
			if _, err := s.db.Exec(`CREATE TRIGGER sabotage_delete BEFORE DELETE ON chunks BEGIN SELECT RAISE(ABORT, 'sabotage'); END;`); err != nil {
				t.Fatalf("create sabotage trigger: %v", err)
			}
			return context.Background()
		}},
		{"context cancelled", func(t *testing.T, s *SQLiteStorage) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage, err := NewSQLiteStorage(filepath.Join(t.TempDir(), "atomic.db"), 4)
			if err != nil {
				t.Fatalf("NewSQLiteStorage: %v", err)
			}
			t.Cleanup(func() {
				if err := storage.Close(); err != nil {
					t.Errorf("close storage: %v", err)
				}
			})
			ctx := context.Background()

			chunk := Chunk{ID: "atomic-1", FilePath: "/r/a.go", Type: ChunkFunction, Name: "A", Language: "go"}
			if err := storage.Create(ctx, chunk, []float32{0.1, 0.2, 0.3, 0.4}); err != nil {
				t.Fatalf("Create: %v", err)
			}
			if err := storage.SetFileHashWithFallback(ctx, "/r/a.go", "h1", FallbackParseFailed); err != nil {
				t.Fatalf("SetFileHashWithFallback: %v", err)
			}

			deleteCtx := tc.sabotage(t, storage)
			if _, err := storage.DeleteByFilePath(deleteCtx, "/r/a.go"); err == nil {
				t.Fatal("DeleteByFilePath succeeded; want the failure surfaced")
			}

			// Both records survive the failed delete.
			if hash, err := storage.GetFileHash(ctx, "/r/a.go"); err != nil || hash != "h1" {
				t.Errorf("hash after the failed delete = %q, %v; want it rolled back", hash, err)
			}
			chunks, err := storage.List(ctx, ListOptions{FilePath: "/r/a.go"})
			if err != nil || len(chunks) != 1 {
				t.Errorf("chunks after the failed delete = %d, %v; want the chunk delete rolled back", len(chunks), err)
			}
		})
	}
}
