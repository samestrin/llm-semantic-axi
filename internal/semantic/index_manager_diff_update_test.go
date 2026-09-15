package semantic

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// versionedEmbedder stamps every embedding it produces with the current version, so a
// test can tell an embedding created in round 1 from one created in round 2.
type versionedEmbedder struct {
	mu        sync.Mutex
	version   float32
	lastBatch []string
	batches   int
}

func (v *versionedEmbedder) setVersion(n float32) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.version = n
}

func (v *versionedEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return []float32{v.version, 0, 0, 0}, nil
}

func (v *versionedEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.batches++
	v.lastBatch = append([]string(nil), texts...)
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{v.version, 0, 0, 0}
	}
	return out, nil
}

func (v *versionedEmbedder) Dimensions() int { return 4 }
func (v *versionedEmbedder) Model() string   { return "versioned-embedder" }

func (v *versionedEmbedder) snapshot() (batches int, last []string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.batches, append([]string(nil), v.lastBatch...)
}

// TestIndexManager_Update_ReusesUnchangedChunkEmbeddings pins the content-aware diff:
// when a file is edited, only the chunks whose content actually changed are re-embedded.
// A function that merely shifted lines keeps the embedding it already had, which is the
// whole point of the diff path - it would be lost if the file were re-embedded wholesale.
func TestIndexManager_Update_ReusesUnchangedChunkEmbeddings(t *testing.T) {
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "sample.go")

	v1 := `package sample

func Alpha() int {
	return 1
}

func Beta() int {
	return 2
}
`
	if err := os.WriteFile(file, []byte(v1), 0644); err != nil {
		t.Fatalf("failed to write v1: %v", err)
	}

	storage := newFileStorage(t)
	embedder := &versionedEmbedder{version: 1}
	mgr := NewIndexManager(storage, embedder, goFactory())
	ctx := context.Background()

	if _, err := mgr.Index(ctx, tmpDir, IndexOptions{}); err != nil {
		t.Fatalf("Index() error = %v", err)
	}

	// Alpha shifts down two lines but is byte-identical; Beta's body changes.
	v2 := `package sample

// a newly added comment

func Alpha() int {
	return 1
}

func Beta() int {
	return 22
}
`
	if err := os.WriteFile(file, []byte(v2), 0644); err != nil {
		t.Fatalf("failed to write v2: %v", err)
	}

	embedder.setVersion(2)
	batchesBefore, _ := embedder.snapshot()

	result, err := mgr.Update(ctx, tmpDir, UpdateOptions{})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if result.FilesUpdated != 1 {
		t.Errorf("FilesUpdated = %d, want 1", result.FilesUpdated)
	}

	batchesAfter, lastBatch := embedder.snapshot()
	if batchesAfter != batchesBefore+1 {
		t.Fatalf("EmbedBatch called %d extra times during Update, want exactly 1", batchesAfter-batchesBefore)
	}
	if len(lastBatch) != 1 {
		t.Fatalf("Update re-embedded %d chunks, want 1 (only Beta changed): %v", len(lastBatch), lastBatch)
	}
	if !strings.Contains(lastBatch[0], "Beta") {
		t.Errorf("re-embedded chunk = %q, want the changed Beta function", truncate(lastBatch[0], 60))
	}

	// Confirm the stored vectors: Alpha keeps its round-1 stamp, Beta gets round 2.
	chunks, err := storage.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("storage holds %d chunks, want 2", len(chunks))
	}
	byName := map[string]string{}
	for _, c := range chunks {
		byName[c.Name] = c.ID
	}
	if byName["Alpha"] == "" || byName["Beta"] == "" {
		t.Fatalf("indexed chunk names = %v, want Alpha and Beta", byName)
	}

	embs, err := storage.ReadEmbeddings(ctx, []string{byName["Alpha"], byName["Beta"]})
	if err != nil {
		t.Fatalf("ReadEmbeddings() error = %v", err)
	}
	if got := embs[byName["Alpha"]]; len(got) == 0 || got[0] != 1 {
		t.Errorf("Alpha embedding = %v, want the round-1 vector [1 0 0 0] reused", got)
	}
	if got := embs[byName["Beta"]]; len(got) == 0 || got[0] != 2 {
		t.Errorf("Beta embedding = %v, want the round-2 vector [2 0 0 0]", got)
	}
}

// TestIndexManager_Update_RemovesDeletedChunks pins that a function deleted from a file
// has its chunk removed from the index by the diff path, not left orphaned.
func TestIndexManager_Update_RemovesDeletedChunks(t *testing.T) {
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "sample.go")

	if err := os.WriteFile(file, []byte(`package sample

func Alpha() int {
	return 1
}

func Gone() int {
	return 9
}
`), 0644); err != nil {
		t.Fatalf("failed to write v1: %v", err)
	}

	storage := newFileStorage(t)
	mgr := NewIndexManager(storage, &versionedEmbedder{version: 1}, goFactory())
	ctx := context.Background()

	if _, err := mgr.Index(ctx, tmpDir, IndexOptions{}); err != nil {
		t.Fatalf("Index() error = %v", err)
	}

	if err := os.WriteFile(file, []byte(`package sample

func Alpha() int {
	return 1
}
`), 0644); err != nil {
		t.Fatalf("failed to write v2: %v", err)
	}

	result, err := mgr.Update(ctx, tmpDir, UpdateOptions{})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if result.ChunksRemoved < 1 {
		t.Errorf("ChunksRemoved = %d, want at least 1 for the deleted function", result.ChunksRemoved)
	}

	chunks, err := storage.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(chunks) != 1 || chunks[0].Name != "Alpha" {
		t.Errorf("remaining chunks = %v, want only Alpha", chunks)
	}
}

// TestIndexManager_Update_SkipsUnchangedFiles pins that a file whose hash is unchanged is
// not re-chunked or re-embedded at all.
func TestIndexManager_Update_SkipsUnchangedFiles(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.go"), goSource("Alpha"), 0644); err != nil {
		t.Fatalf("failed to write sample.go: %v", err)
	}

	storage := newFileStorage(t)
	embedder := &versionedEmbedder{version: 1}
	mgr := NewIndexManager(storage, embedder, goFactory())
	ctx := context.Background()

	if _, err := mgr.Index(ctx, tmpDir, IndexOptions{}); err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	batchesBefore, _ := embedder.snapshot()

	result, err := mgr.Update(ctx, tmpDir, UpdateOptions{})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if result.FilesUpdated != 0 {
		t.Errorf("FilesUpdated = %d, want 0 for an untouched tree", result.FilesUpdated)
	}

	batchesAfter, _ := embedder.snapshot()
	if batchesAfter != batchesBefore {
		t.Errorf("EmbedBatch was called %d extra times for an untouched tree, want 0", batchesAfter-batchesBefore)
	}
}
