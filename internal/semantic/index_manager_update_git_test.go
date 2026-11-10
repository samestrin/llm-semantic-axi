package semantic

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpdateGit_FailedFileKeepsItsOldChunks defends a changed file that fails
// to re-index under `index-update`: its old chunks were deleted before the new
// ones were embedded, so a file named in Errors had also dropped out of search.
// The file must stay searchable as it was indexed until a re-index succeeds.
func TestUpdateGit_FailedFileKeepsItsOldChunks(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")
	ctx := context.Background()

	storage := newFileStorage(t)
	embedder := &failingEmbedder{trackingEmbedder: trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, marker: "Broken"}
	mgr := NewIndexManager(storage, embedder, goFactory())
	if _, err := mgr.Index(ctx, repo, IndexOptions{}); err != nil {
		t.Fatalf("Index: %v", err)
	}
	path := filepath.Join(repo, "one.go")
	before, err := storage.GetChunksByFilePath(ctx, path)
	if err != nil || len(before) == 0 {
		t.Fatalf("one.go indexed no chunks (err %v); the test needs some to keep", err)
	}

	// The edit renames the function to one the embedder rejects.
	commitGoFile(t, repo, "one.go", "Broken")
	result, err := mgr.UpdateGit(ctx, repo, "HEAD~1", UpdateOptions{Domain: "code"})
	if err != nil {
		t.Fatalf("UpdateGit: %v", err)
	}
	if result.FilesSkipped != 1 || len(result.Errors) != 1 || !strings.Contains(result.Errors[0], path) {
		t.Fatalf("UpdateGit skipped %d, errors %v; the test needs one.go to fail", result.FilesSkipped, result.Errors)
	}
	if result.ChunksRemoved != 0 {
		t.Errorf("ChunksRemoved = %d for a file that failed, want 0", result.ChunksRemoved)
	}

	after, err := storage.GetChunksByFilePath(ctx, path)
	if err != nil {
		t.Fatalf("GetChunksByFilePath: %v", err)
	}
	if len(after) != len(before) || after[0].Name != "One" {
		t.Errorf("one.go chunks after the failed update = %+v, want the old ones %+v", after, before)
	}

	// Once the embedder accepts it, the update replaces the old chunks.
	embedder.marker = "\x00never"
	result, err = mgr.UpdateGit(ctx, repo, "HEAD~1", UpdateOptions{Domain: "code"})
	if err != nil {
		t.Fatalf("UpdateGit: %v", err)
	}
	if result.FilesUpdated != 1 || result.ChunksRemoved == 0 {
		t.Errorf("clean UpdateGit updated %d files, removed %d chunks; want 1 and the old chunks", result.FilesUpdated, result.ChunksRemoved)
	}
	after, err = storage.GetChunksByFilePath(ctx, path)
	if err != nil {
		t.Fatalf("GetChunksByFilePath: %v", err)
	}
	for _, c := range after {
		if c.Name == "One" {
			t.Errorf("the old chunk survived a successful update: %+v", after)
		}
	}
}
