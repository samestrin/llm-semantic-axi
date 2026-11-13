package semantic

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// deleteErrStorage wraps a real storage whose DeleteByFilePath always fails.
type deleteErrStorage struct {
	Storage
	err error
}

func (s *deleteErrStorage) DeleteByFilePath(ctx context.Context, filePath string) (int, error) {
	return 0, s.err
}

// createBatchErrStorage wraps a real storage whose next failures CreateBatch
// calls fail; a negative count fails every call.
type createBatchErrStorage struct {
	Storage
	err      error
	failures int
}

func (s *createBatchErrStorage) CreateBatch(ctx context.Context, chunks []ChunkWithEmbedding) error {
	if s.failures != 0 {
		if s.failures > 0 {
			s.failures--
		}
		return s.err
	}
	return s.Storage.CreateBatch(ctx, chunks)
}

// TestUpdate_FailedDeleteOfARemovedFileIsReported defends the full-scan update
// against counting a removal that never happened: the delete error was
// discarded, FilesRemoved counted the file, and the last index was recorded as
// if the index matched the tree, while the file's chunks were still searchable.
func TestUpdate_FailedDeleteOfARemovedFileIsReported(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone.go")
	if err := os.WriteFile(gone, goSource("Gone"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	inner := newFileStorage(t)
	if _, err := NewIndexManager(inner, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory()).Index(ctx, dir, IndexOptions{}); err != nil {
		t.Fatalf("Index: %v", err)
	}
	sentinel := LastIndex{Commit: "sentinel"}
	if err := inner.SetLastIndex(ctx, sentinel); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("delete failed")
	mgr := NewIndexManager(&deleteErrStorage{Storage: inner, err: boom}, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())
	result, err := mgr.Update(ctx, dir, UpdateOptions{})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if result.FilesRemoved != 0 {
		t.Errorf("FilesRemoved = %d for a delete that failed, want 0", result.FilesRemoved)
	}
	if result.FilesSkipped != 1 || len(result.Errors) != 1 || !strings.Contains(result.Errors[0], gone) || !strings.Contains(result.Errors[0], "delete failed") {
		t.Errorf("skipped %d, errors %v; want gone.go named with the delete error", result.FilesSkipped, result.Errors)
	}
	if got := mustLastIndex(t, inner); got == nil || got.Commit != sentinel.Commit {
		t.Errorf("last index = %+v after a failed removal, want it left at %q", got, sentinel.Commit)
	}
}

// TestUpdateGit_FailedDeleteIsReported defends the git update against the same
// discarded error, for a file git reports deleted and for a changed file whose
// old chunks could not be removed before its new ones were stored. Either way
// the file was not brought up to date, so it is named and the last index stays.
func TestUpdateGit_FailedDeleteIsReported(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(t *testing.T, repo string)
	}{
		{"deleted file", func(t *testing.T, repo string) {
			gitRun(t, repo, "rm", "-q", "one.go")
			gitRun(t, repo, "commit", "-m", "remove one")
		}},
		{"changed file", func(t *testing.T, repo string) {
			commitGoFile(t, repo, "one.go", "Changed")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			gitRun(t, repo, "init", "-q")
			commitGoFile(t, repo, "one.go", "One")
			ctx := context.Background()
			inner := newFileStorage(t)
			if _, err := NewIndexManager(inner, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory()).Index(ctx, repo, IndexOptions{}); err != nil {
				t.Fatalf("Index: %v", err)
			}
			recorded := mustLastIndex(t, inner)
			if recorded == nil || recorded.Commit == "" {
				t.Fatalf("Index recorded no commit (%+v); the test needs one to advance from", recorded)
			}
			tc.edit(t, repo)

			path := filepath.Join(repo, "one.go")
			mgr := NewIndexManager(&deleteErrStorage{Storage: inner, err: errors.New("delete failed")}, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())
			result, err := mgr.UpdateGit(ctx, repo, "HEAD~1", UpdateOptions{Domain: "code"})
			if err != nil {
				t.Fatalf("UpdateGit: %v", err)
			}
			if result.FilesRemoved != 0 || result.FilesUpdated != 0 {
				t.Errorf("removed %d, updated %d files; a failed delete brought none up to date", result.FilesRemoved, result.FilesUpdated)
			}
			if result.FilesSkipped != 1 || len(result.Errors) != 1 || !strings.Contains(result.Errors[0], path) || !strings.Contains(result.Errors[0], "delete failed") {
				t.Errorf("skipped %d, errors %v; want one.go named with the delete error", result.FilesSkipped, result.Errors)
			}
			if got := mustLastIndex(t, inner); got == nil || got.Commit != recorded.Commit {
				t.Errorf("last index = %+v after a failed delete, want it left at %s", got, recorded.Commit)
			}
			chunks, err := inner.GetChunksByFilePath(ctx, path)
			if err != nil || len(chunks) != 1 || chunks[0].Name != "One" {
				t.Errorf("one.go chunks = %+v (err %v), want only the old One chunk", chunks, err)
			}
		})
	}
}

// TestUpdateGit_FailedStoreKeepsTheOldChunks defends a changed file whose new
// chunks could not be stored: its old chunks had already been deleted, so the
// file was named as skipped while it had dropped out of search. The old chunks
// and hash must be back, and the next update must still re-index the file.
func TestUpdateGit_FailedStoreKeepsTheOldChunks(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-q")
	commitGoFile(t, repo, "one.go", "One")
	ctx := context.Background()
	inner := newFileStorage(t)
	embedder := &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}
	if _, err := NewIndexManager(inner, embedder, goFactory()).Index(ctx, repo, IndexOptions{}); err != nil {
		t.Fatalf("Index: %v", err)
	}
	path := filepath.Join(repo, "one.go")
	oldHash, err := inner.GetFileHash(ctx, path)
	if err != nil || oldHash == "" {
		t.Fatalf("one.go has no stored hash (%q, %v)", oldHash, err)
	}
	commitGoFile(t, repo, "one.go", "Two")

	failing := &createBatchErrStorage{Storage: inner, err: errors.New("disk full"), failures: 1}
	mgr := NewIndexManager(failing, embedder, goFactory())
	result, err := mgr.UpdateGit(ctx, repo, "HEAD~1", UpdateOptions{Domain: "code"})
	if err != nil {
		t.Fatalf("UpdateGit: %v", err)
	}
	if result.FilesSkipped != 1 || len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "disk full") {
		t.Fatalf("skipped %d, errors %v; the test needs one.go to fail on store", result.FilesSkipped, result.Errors)
	}
	if result.ChunksRemoved != 0 || result.FilesUpdated != 0 {
		t.Errorf("removed %d chunks, updated %d files for a file whose store failed, want 0", result.ChunksRemoved, result.FilesUpdated)
	}
	chunks, err := inner.GetChunksByFilePath(ctx, path)
	if err != nil || len(chunks) != 1 || chunks[0].Name != "One" {
		t.Errorf("one.go chunks after the failed store = %+v (err %v), want the old One chunk", chunks, err)
	}
	if hash, err := inner.GetFileHash(ctx, path); err != nil || hash != oldHash {
		t.Errorf("one.go hash = %q (err %v), want the old %q so it is listed and re-indexed", hash, err, oldHash)
	}

	// Once storage accepts it, the update replaces the old chunks.
	result, err = mgr.UpdateGit(ctx, repo, "HEAD~1", UpdateOptions{Domain: "code"})
	if err != nil {
		t.Fatalf("UpdateGit: %v", err)
	}
	if result.FilesUpdated != 1 || result.FilesSkipped != 0 {
		t.Errorf("clean UpdateGit updated %d, skipped %d (%v); want 1 and 0", result.FilesUpdated, result.FilesSkipped, result.Errors)
	}
	chunks, err = inner.GetChunksByFilePath(ctx, path)
	if err != nil || len(chunks) != 1 || chunks[0].Name != "Two" {
		t.Errorf("one.go chunks after the clean update = %+v (err %v), want only Two", chunks, err)
	}
}

// TestUpdateGit_FailedRestoreSaysTheFileIsUnsearchable defends the case where
// the old chunks cannot be put back either: the error must say the file has
// dropped out of search, not only that the store failed.
func TestUpdateGit_FailedRestoreSaysTheFileIsUnsearchable(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-q")
	commitGoFile(t, repo, "one.go", "One")
	ctx := context.Background()
	inner := newFileStorage(t)
	embedder := &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}
	if _, err := NewIndexManager(inner, embedder, goFactory()).Index(ctx, repo, IndexOptions{}); err != nil {
		t.Fatalf("Index: %v", err)
	}
	commitGoFile(t, repo, "one.go", "Two")

	mgr := NewIndexManager(&createBatchErrStorage{Storage: inner, err: errors.New("disk full"), failures: -1}, embedder, goFactory())
	result, err := mgr.UpdateGit(ctx, repo, "HEAD~1", UpdateOptions{Domain: "code"})
	if err != nil {
		t.Fatalf("UpdateGit: %v", err)
	}
	if result.FilesSkipped != 1 || len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "not searchable") {
		t.Errorf("skipped %d, errors %v; want one.go named as not searchable", result.FilesSkipped, result.Errors)
	}
	if result.ChunksRemoved != 1 {
		t.Errorf("ChunksRemoved = %d, want the 1 old chunk that is gone", result.ChunksRemoved)
	}
}

// faultStorage wraps a real SQLiteStorage and fails the nth call of a named
// method (counting from 1), so each read the F2 recovery path depends on can
// be failed on its own. It keeps the FallbackStore methods, which the
// recovery path reads and writes through.
type faultStorage struct {
	*SQLiteStorage
	failOn map[string]int // method name -> call number to fail
	calls  map[string]int
	// noEmbeddings makes ReadEmbeddings return no rows without an error.
	noEmbeddings bool
}

var errFault = errors.New("injected fault")

func newFaultStorage(inner *SQLiteStorage, failOn map[string]int) *faultStorage {
	return &faultStorage{SQLiteStorage: inner, failOn: failOn, calls: map[string]int{}}
}

func (s *faultStorage) fault(method string) error {
	s.calls[method]++
	if n, ok := s.failOn[method]; ok && n == s.calls[method] {
		return errFault
	}
	return nil
}

func (s *faultStorage) List(ctx context.Context, opts ListOptions) ([]Chunk, error) {
	if err := s.fault("List"); err != nil {
		return nil, err
	}
	return s.SQLiteStorage.List(ctx, opts)
}

func (s *faultStorage) ReadEmbeddings(ctx context.Context, ids []string) (map[string][]float32, error) {
	if err := s.fault("ReadEmbeddings"); err != nil {
		return nil, err
	}
	if s.noEmbeddings {
		return map[string][]float32{}, nil
	}
	return s.SQLiteStorage.ReadEmbeddings(ctx, ids)
}

func (s *faultStorage) GetFileHash(ctx context.Context, filePath string) (string, error) {
	if err := s.fault("GetFileHash"); err != nil {
		return "", err
	}
	return s.SQLiteStorage.GetFileHash(ctx, filePath)
}

func (s *faultStorage) ListFallbackFiles(ctx context.Context) (map[string]FallbackReason, error) {
	if err := s.fault("ListFallbackFiles"); err != nil {
		return nil, err
	}
	return s.SQLiteStorage.ListFallbackFiles(ctx)
}

func (s *faultStorage) GetFallbackReason(ctx context.Context, filePath string) (FallbackReason, error) {
	if err := s.fault("GetFallbackReason"); err != nil {
		return FallbackNone, err
	}
	return s.SQLiteStorage.GetFallbackReason(ctx, filePath)
}

func (s *faultStorage) DeleteByFilePath(ctx context.Context, filePath string) (int, error) {
	if err := s.fault("DeleteByFilePath"); err != nil {
		return 0, err
	}
	return s.SQLiteStorage.DeleteByFilePath(ctx, filePath)
}

func (s *faultStorage) CreateBatch(ctx context.Context, chunks []ChunkWithEmbedding) error {
	if err := s.fault("CreateBatch"); err != nil {
		return err
	}
	return s.SQLiteStorage.CreateBatch(ctx, chunks)
}

// gitUpdateFixture indexes one.go (function One) in a fresh repository over a
// real sqlite database, then commits an edit renaming it to Two.
type gitUpdateFixture struct {
	repo, path, dbPath, oldHash string
	inner                       *SQLiteStorage
	recorded                    *LastIndex
}

func newGitUpdateFixture(t *testing.T) *gitUpdateFixture {
	t.Helper()
	f := &gitUpdateFixture{repo: t.TempDir()}
	gitRun(t, f.repo, "init", "-q")
	commitGoFile(t, f.repo, "one.go", "One")
	f.dbPath = filepath.Join(t.TempDir(), "index.db")
	inner, err := NewSQLiteStorage(f.dbPath, 4)
	if err != nil {
		t.Fatalf("NewSQLiteStorage: %v", err)
	}
	t.Cleanup(func() {
		if err := inner.Close(); err != nil {
			t.Errorf("close storage: %v", err)
		}
	})
	f.inner = inner
	ctx := context.Background()
	if _, err := NewIndexManager(inner, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory()).Index(ctx, f.repo, IndexOptions{}); err != nil {
		t.Fatalf("Index: %v", err)
	}
	f.path = filepath.Join(f.repo, "one.go")
	if f.oldHash, err = inner.GetFileHash(ctx, f.path); err != nil || f.oldHash == "" {
		t.Fatalf("one.go has no stored hash (%q, %v)", f.oldHash, err)
	}
	if f.recorded = mustLastIndex(t, inner); f.recorded == nil || f.recorded.Commit == "" {
		t.Fatalf("Index recorded no commit (%+v)", f.recorded)
	}
	commitGoFile(t, f.repo, "one.go", "Two")
	return f
}

func (f *gitUpdateFixture) update(t *testing.T, storage Storage) *UpdateResult {
	t.Helper()
	mgr := NewIndexManager(storage, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())
	result, err := mgr.UpdateGit(context.Background(), f.repo, "HEAD~1", UpdateOptions{Domain: "code"})
	if err != nil {
		t.Fatalf("UpdateGit: %v", err)
	}
	return result
}

// assertSkippedAndUntouched checks one.go was named as skipped with want in
// its error, and that its old chunk, hash and the last index are as they were.
func (f *gitUpdateFixture) assertSkippedAndUntouched(t *testing.T, result *UpdateResult, want string) {
	t.Helper()
	ctx := context.Background()
	if result.FilesSkipped != 1 || len(result.Errors) != 1 || !strings.Contains(result.Errors[0], f.path) || !strings.Contains(result.Errors[0], want) {
		t.Errorf("skipped %d, errors %v; want one.go named with %q", result.FilesSkipped, result.Errors, want)
	}
	if result.FilesUpdated != 0 || result.ChunksRemoved != 0 {
		t.Errorf("updated %d files, removed %d chunks; want 0 and 0", result.FilesUpdated, result.ChunksRemoved)
	}
	chunks, err := f.inner.GetChunksByFilePath(ctx, f.path)
	if err != nil || len(chunks) != 1 || chunks[0].Name != "One" {
		t.Errorf("one.go chunks = %+v (err %v), want the old One chunk", chunks, err)
	}
	if hash, err := f.inner.GetFileHash(ctx, f.path); err != nil || hash != f.oldHash {
		t.Errorf("one.go hash = %q (err %v), want the old %q", hash, err, f.oldHash)
	}
	if got := mustLastIndex(t, f.inner); got == nil || got.Commit != f.recorded.Commit {
		t.Errorf("last index = %+v, want it left at %s", got, f.recorded.Commit)
	}
}

// TestUpdateGit_UnreadableOldStateLeavesTheFileAlone defends the snapshot
// guards: if any read of a file's indexed state fails, the old chunks must
// not be deleted, since a later store failure would then have nothing to put
// back and the file would drop out of search while reported only as skipped.
func TestUpdateGit_UnreadableOldStateLeavesTheFileAlone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failOn map[string]int
		noEmb  bool
		want   string
	}{
		{"list chunks", map[string]int{"List": 1}, false, "failed to read the old chunks"},
		{"read embeddings", map[string]int{"ReadEmbeddings": 1}, false, "failed to read the old embeddings"},
		{"missing embedding", nil, true, "has no stored embedding"},
		// The first GetFileHash is fileNeedsUpdate's; the second is the snapshot's.
		{"read hash", map[string]int{"GetFileHash": 2}, false, "failed to read stored hash"},
		{"read fallback reason", map[string]int{"GetFallbackReason": 1}, false, "failed to read the fallback reason"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGitUpdateFixture(t)
			storage := newFaultStorage(f.inner, tc.failOn)
			storage.noEmbeddings = tc.noEmb
			result := f.update(t, storage)
			f.assertSkippedAndUntouched(t, result, tc.want)
			if storage.calls["DeleteByFilePath"] != 0 {
				t.Errorf("DeleteByFilePath called %d times after the old state could not be read, want 0", storage.calls["DeleteByFilePath"])
			}
		})
	}
}

// TestUpdateGit_SnapshotReadsOnlyTheChangedFilesFallback defends the cost of
// an incremental update: snapshotting a changed file must read that file's
// fallback reason, not list every fallen-back file in the index, which made
// UpdateGit O(changed x indexed). The reason it reads must still be restored
// when the store fails.
func TestUpdateGit_SnapshotReadsOnlyTheChangedFilesFallback(t *testing.T) {
	f := newGitUpdateFixture(t)
	ctx := context.Background()
	if err := f.inner.SetFileHashWithFallback(ctx, f.path, f.oldHash, FallbackParseFailed); err != nil {
		t.Fatalf("SetFileHashWithFallback: %v", err)
	}
	storage := newFaultStorage(f.inner, map[string]int{"CreateBatch": 1})
	result := f.update(t, storage)
	f.assertSkippedAndUntouched(t, result, errFault.Error())
	if n := storage.calls["ListFallbackFiles"]; n != 0 {
		t.Errorf("ListFallbackFiles called %d times during UpdateGit, want 0", n)
	}
	if n := storage.calls["GetFallbackReason"]; n != 1 {
		t.Errorf("GetFallbackReason called %d times for one changed file, want 1", n)
	}
	if reason, err := f.inner.GetFallbackReason(ctx, f.path); err != nil || reason != FallbackParseFailed {
		t.Errorf("restored fallback reason = %q, %v; want %q", reason, err, FallbackParseFailed)
	}
}

// TestUpdateGit_RestoreDeleteFailureSaysNotSearchable defends restoreFile's
// first step: if clearing the failed store cannot be done, nothing is put
// back, and the error must say the file is not searchable.
func TestUpdateGit_RestoreDeleteFailureSaysNotSearchable(t *testing.T) {
	f := newGitUpdateFixture(t)
	storage := newFaultStorage(f.inner, map[string]int{"CreateBatch": 1, "DeleteByFilePath": 2})
	result := f.update(t, storage)
	if result.FilesSkipped != 1 || len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "not searchable") || !strings.Contains(result.Errors[0], errFault.Error()) {
		t.Errorf("skipped %d, errors %v; want one.go named as not searchable with the delete error", result.FilesSkipped, result.Errors)
	}
	if storage.calls["CreateBatch"] != 1 {
		t.Errorf("CreateBatch called %d times; restore must stop at the failed delete", storage.calls["CreateBatch"])
	}
	if got := mustLastIndex(t, f.inner); got == nil || got.Commit != f.recorded.Commit {
		t.Errorf("last index = %+v, want it left at %s", got, f.recorded.Commit)
	}
}

// TestUpdateGit_RestoreWithNoOldHashWritesNone defends restoreFile's
// empty-hash return: a file indexed with chunks but no hash row gets its
// chunks back and no invented hash, so the next update still re-indexes it.
func TestUpdateGit_RestoreWithNoOldHashWritesNone(t *testing.T) {
	f := newGitUpdateFixture(t)
	db, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM file_hashes WHERE file_path = ?`, f.path); err != nil {
		t.Fatalf("drop hash row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close index: %v", err)
	}

	result := f.update(t, newFaultStorage(f.inner, map[string]int{"CreateBatch": 1}))
	if result.FilesSkipped != 1 || len(result.Errors) != 1 || strings.Contains(result.Errors[0], "not searchable") {
		t.Errorf("skipped %d, errors %v; want one.go skipped with its old chunks restored", result.FilesSkipped, result.Errors)
	}
	ctx := context.Background()
	chunks, err := f.inner.GetChunksByFilePath(ctx, f.path)
	if err != nil || len(chunks) != 1 || chunks[0].Name != "One" {
		t.Errorf("one.go chunks = %+v (err %v), want the old One chunk back", chunks, err)
	}
	// An empty hash row also reads back as "", so ask whether a row exists.
	if indexed, err := f.inner.ListIndexedFiles(ctx); err != nil || indexed[f.path] {
		t.Errorf("one.go has a hash row after restoring a file that had none (err %v)", err)
	}
}
