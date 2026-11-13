package semantic

import (
	"context"
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
