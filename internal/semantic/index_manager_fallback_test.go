package semantic

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fallbackStub stands in for treesitter.Fallback, which this package cannot
// import: content containing "BROKEN" is reported as having fallen back.
type fallbackStub struct{}

func (fallbackStub) SupportedExtensions() []string { return []string{"fb"} }

func (s fallbackStub) Chunk(path string, content []byte) ([]Chunk, error) {
	chunks, _, err := s.ChunkWithFallback(path, content)
	return chunks, err
}

func (fallbackStub) ChunkWithFallback(path string, content []byte) ([]Chunk, bool, error) {
	c := Chunk{FilePath: path, Type: ChunkFile, Name: filepath.Base(path), Content: string(content), StartLine: 1, EndLine: 1, Language: "fb"}
	c.ID = c.GenerateID()
	return []Chunk{c}, strings.Contains(string(content), "BROKEN"), nil
}

func fallbackFixture(t *testing.T, files map[string]string) (string, *IndexManager) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	factory := NewChunkerFactory()
	factory.Register("fb", fallbackStub{})
	mgr := NewIndexManager(newFileStorage(t), &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, factory)
	return dir, mgr
}

// Defends AC2 through both index paths: a fallen-back file must be counted
// and named in the result, or the output has nothing to state. The cross-file
// batching path chunks through a different function from the per-file path,
// so each needs its own check.
func TestIndex_CountsFilesThatFellBack(t *testing.T) {
	for name, opts := range map[string]IndexOptions{
		"per-file":   {},
		"cross-file": {EmbedBatchSize: 10},
	} {
		t.Run(name, func(t *testing.T) {
			dir, mgr := fallbackFixture(t, map[string]string{"good.fb": "fine", "bad.fb": "BROKEN"})

			result, err := mgr.Index(context.Background(), dir, opts)
			if err != nil {
				t.Fatalf("Index: %v", err)
			}
			if result.FilesFallback != 1 {
				t.Errorf("FilesFallback = %d, want 1", result.FilesFallback)
			}
			want := filepath.Join(dir, "bad.fb")
			if len(result.FallbackFiles) != 1 || result.FallbackFiles[0] != want {
				t.Errorf("FallbackFiles = %v, want [%s]", result.FallbackFiles, want)
			}
			if result.FilesProcessed != 2 {
				t.Errorf("FilesProcessed = %d, want 2: a fallen-back file is still indexed", result.FilesProcessed)
			}
		})
	}
}

// Defends "a clean fixture reports none": a count that is non-zero for clean
// files would be noise nobody reads.
func TestIndex_CleanFixtureCountsNoFallback(t *testing.T) {
	dir, mgr := fallbackFixture(t, map[string]string{"good.fb": "fine"})

	result, err := mgr.Index(context.Background(), dir, IndexOptions{})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if result.FilesFallback != 0 || len(result.FallbackFiles) != 0 {
		t.Errorf("clean fixture reported fallback: %d %v", result.FilesFallback, result.FallbackFiles)
	}
}

// Defends the full-scan index-update path: an edit that breaks a file's
// syntax must be counted there too, not only on a full index.
func TestUpdate_CountsFilesThatFellBack(t *testing.T) {
	dir, mgr := fallbackFixture(t, map[string]string{"good.fb": "fine", "bad.fb": "fine too"})
	ctx := context.Background()
	if _, err := mgr.Index(ctx, dir, IndexOptions{}); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.fb"), []byte("now BROKEN"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := mgr.Update(ctx, dir, UpdateOptions{})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if result.FilesFallback != 1 {
		t.Errorf("FilesFallback = %d, want 1", result.FilesFallback)
	}
	if len(result.FallbackFiles) != 1 || result.FallbackFiles[0] != filepath.Join(dir, "bad.fb") {
		t.Errorf("FallbackFiles = %v, want bad.fb", result.FallbackFiles)
	}
}

// Defends the git index-update path, which chunks through processFile rather
// than the diff path the full scan uses.
func TestUpdateGit_CountsFilesThatFellBack(t *testing.T) {
	dir, mgr := fallbackFixture(t, map[string]string{"good.fb": "fine", "bad.fb": "fine too"})
	gitRun(t, dir, "init")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "initial")
	ctx := context.Background()
	if _, err := mgr.Index(ctx, dir, IndexOptions{}); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.fb"), []byte("now BROKEN"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "commit", "-am", "break it")

	result, err := mgr.UpdateGit(ctx, dir, "HEAD~1", UpdateOptions{})
	if err != nil {
		t.Fatalf("UpdateGit: %v", err)
	}
	if result.FilesFallback != 1 {
		t.Errorf("FilesFallback = %d, want 1", result.FilesFallback)
	}
	if len(result.FallbackFiles) != 1 || result.FallbackFiles[0] != filepath.Join(dir, "bad.fb") {
		t.Errorf("FallbackFiles = %v, want bad.fb", result.FallbackFiles)
	}
}
