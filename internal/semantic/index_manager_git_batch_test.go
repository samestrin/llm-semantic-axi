package semantic

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// goSource returns a small compilable Go file with one function of the given name.
func goSource(fnName string) []byte {
	return []byte("package sample\n\nfunc " + fnName + "(a, b int) int {\n\treturn a + b\n}\n")
}

// newFileStorage returns a file-backed SQLiteStorage; the index manager writes from
// several goroutines, which a ":memory:" database cannot share across pooled connections.
func newFileStorage(t *testing.T) *SQLiteStorage {
	t.Helper()
	storage, err := NewSQLiteStorage(filepath.Join(t.TempDir(), "index.db"), 4)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	return storage
}

func goFactory() *ChunkerFactory {
	factory := NewChunkerFactory()
	factory.Register("go", NewGoChunker())
	return factory
}

// TestIndexManager_CrossFileBatching pins the EmbedBatchSize path: chunks from several
// files are grouped into embed batches of at least the requested size, every chunk still
// lands in storage, and the per-file hashes are committed so a re-run skips the files.
func TestIndexManager_CrossFileBatching(t *testing.T) {
	tmpDir := t.TempDir()
	for _, name := range []string{"one.go", "two.go", "three.go", "four.go"} {
		fn := strings.TrimSuffix(name, ".go")
		if err := os.WriteFile(filepath.Join(tmpDir, name), goSource("Fn"+fn), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}

	storage := newFileStorage(t)
	embedder := &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}
	mgr := NewIndexManager(storage, embedder, goFactory())
	ctx := context.Background()

	var progressEvents, uploadEvents int
	result, err := mgr.Index(ctx, tmpDir, IndexOptions{
		EmbedBatchSize:   2,
		OnProgress:       func(ProgressEvent) { progressEvents++ },
		OnUploadProgress: func(UploadProgressEvent) { uploadEvents++ },
	})
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}

	if result.FilesProcessed != 4 {
		t.Errorf("FilesProcessed = %d, want 4", result.FilesProcessed)
	}
	if result.ChunksCreated != 4 {
		t.Errorf("ChunksCreated = %d, want 4 (one function per file)", result.ChunksCreated)
	}
	if progressEvents != 4 {
		t.Errorf("OnProgress fired %d times, want once per file", progressEvents)
	}
	if uploadEvents == 0 {
		t.Error("OnUploadProgress never fired for a cross-file batched index")
	}

	// A batch is cut once it holds EmbedBatchSize chunks, so 4 chunks make 2 calls of 2.
	sizes := embedder.CallSizes()
	if len(sizes) != 2 {
		t.Fatalf("EmbedBatch called %d times with sizes %v, want 2 calls", len(sizes), sizes)
	}
	for _, s := range sizes {
		if s != 2 {
			t.Errorf("embed batch size = %d, want 2 (EmbedBatchSize)", s)
		}
	}

	stored, err := storage.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(stored) != 4 {
		t.Errorf("storage holds %d chunks, want 4", len(stored))
	}

	// Hashes were committed, so a second run without Force re-indexes nothing.
	second, err := mgr.Index(ctx, tmpDir, IndexOptions{EmbedBatchSize: 2})
	if err != nil {
		t.Fatalf("second Index() error = %v", err)
	}
	if second.FilesUnchanged != 4 {
		t.Errorf("FilesUnchanged = %d on a re-run, want 4", second.FilesUnchanged)
	}
	if second.ChunksCreated != 0 {
		t.Errorf("ChunksCreated = %d on a re-run, want 0", second.ChunksCreated)
	}
}

// TestIndexManager_CrossFileBatching_ParallelSubBatches pins the parallel upload branch:
// with BatchSize and Parallel both set the chunks are split into concurrent CreateBatch
// calls, and every chunk must still arrive exactly once.
func TestIndexManager_CrossFileBatching_ParallelSubBatches(t *testing.T) {
	tmpDir := t.TempDir()
	for i := 0; i < 6; i++ {
		name := filepath.Join(tmpDir, string(rune('a'+i))+".go")
		if err := os.WriteFile(name, goSource("Fn"+string(rune('A'+i))), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}

	storage := newFileStorage(t)
	mgr := NewIndexManager(storage, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())
	ctx := context.Background()

	result, err := mgr.Index(ctx, tmpDir, IndexOptions{
		EmbedBatchSize: 6,
		BatchSize:      2,
		Parallel:       3,
		Domain:         "docs",
	})
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if result.ChunksCreated != 6 {
		t.Errorf("ChunksCreated = %d, want 6", result.ChunksCreated)
	}

	stored, err := storage.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(stored) != 6 {
		t.Fatalf("storage holds %d chunks, want 6 - a parallel sub-batch was dropped or duplicated", len(stored))
	}
	for _, c := range stored {
		if c.Domain != "docs" {
			t.Errorf("chunk %s Domain = %q, want \"docs\" from IndexOptions", c.ID, c.Domain)
		}
	}
}

// TestIndexManager_CrossFileBatching_SkipsUnchunkableFiles pins that a file the chunker
// rejects is recorded as skipped with an error message rather than aborting the batch.
func TestIndexManager_CrossFileBatching_SkipsUnchunkableFiles(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "good.go"), goSource("Good"), 0644); err != nil {
		t.Fatalf("failed to write good.go: %v", err)
	}
	// Valid extension, unparseable content.
	if err := os.WriteFile(filepath.Join(tmpDir, "bad.go"), []byte("this is not go at all {{{"), 0644); err != nil {
		t.Fatalf("failed to write bad.go: %v", err)
	}

	storage := newFileStorage(t)
	mgr := NewIndexManager(storage, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())

	result, err := mgr.Index(context.Background(), tmpDir, IndexOptions{EmbedBatchSize: 4})
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if result.FilesSkipped != 1 {
		t.Errorf("FilesSkipped = %d, want 1", result.FilesSkipped)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "bad.go") {
		t.Errorf("Errors = %v, want one entry naming bad.go", result.Errors)
	}
	if result.FilesProcessed != 1 {
		t.Errorf("FilesProcessed = %d, want 1 (good.go still indexed)", result.FilesProcessed)
	}
}

// gitRun runs a git command in dir with a deterministic identity.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// TestIndexManager_UpdateGit pins the git-aware incremental update: only files git
// reports as changed are re-indexed, files git reports as deleted have their chunks
// removed, and untouched files are left alone.
func TestIndexManager_UpdateGit(t *testing.T) {
	tmpDir := t.TempDir()
	gitRun(t, tmpDir, "init")

	write := func(name string, fn string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(tmpDir, name), goSource(fn), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}
	write("keep.go", "Keep")
	write("change.go", "Change")
	write("drop.go", "Drop")
	gitRun(t, tmpDir, "add", ".")
	gitRun(t, tmpDir, "commit", "-m", "initial")

	storage := newFileStorage(t)
	mgr := NewIndexManager(storage, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())
	ctx := context.Background()

	if _, err := mgr.Index(ctx, tmpDir, IndexOptions{}); err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	before, err := storage.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(before) != 3 {
		t.Fatalf("initial index stored %d chunks, want 3", len(before))
	}

	// One modified file, one deleted file, one new file; keep.go is untouched.
	write("change.go", "ChangeRenamed")
	if err := os.Remove(filepath.Join(tmpDir, "drop.go")); err != nil {
		t.Fatalf("failed to remove drop.go: %v", err)
	}
	write("added.go", "Added")
	gitRun(t, tmpDir, "add", "-A")
	gitRun(t, tmpDir, "commit", "-m", "second")

	result, err := mgr.UpdateGit(ctx, tmpDir, "HEAD~1", UpdateOptions{})
	if err != nil {
		t.Fatalf("UpdateGit() error = %v", err)
	}

	if result.FilesUpdated != 2 {
		t.Errorf("FilesUpdated = %d, want 2 (change.go and added.go)", result.FilesUpdated)
	}
	if result.FilesRemoved != 1 {
		t.Errorf("FilesRemoved = %d, want 1 (drop.go)", result.FilesRemoved)
	}
	if result.ChunksCreated != 2 {
		t.Errorf("ChunksCreated = %d, want 2", result.ChunksCreated)
	}

	after, err := storage.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	names := map[string]bool{}
	for _, c := range after {
		names[c.Name] = true
	}
	if !names["ChangeRenamed"] {
		t.Error("the modified file was not re-indexed under its new function name")
	}
	if names["Change"] {
		t.Error("the stale chunk from change.go was not removed")
	}
	if names["Drop"] {
		t.Error("chunks from the deleted file were not removed")
	}
	if !names["Keep"] || !names["Added"] {
		t.Errorf("indexed names = %v, want Keep and Added present", names)
	}
}

// TestIndexManager_UpdateGit_HonoursExcludes pins that the include/exclude filters run
// before the re-index, so an excluded file git reports as changed is left untouched.
func TestIndexManager_UpdateGit_HonoursExcludes(t *testing.T) {
	tmpDir := t.TempDir()
	gitRun(t, tmpDir, "init")

	if err := os.WriteFile(filepath.Join(tmpDir, "app.go"), goSource("App"), 0644); err != nil {
		t.Fatalf("failed to write app.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "app_test.go"), goSource("AppTest"), 0644); err != nil {
		t.Fatalf("failed to write app_test.go: %v", err)
	}
	gitRun(t, tmpDir, "add", ".")
	gitRun(t, tmpDir, "commit", "-m", "initial")

	storage := newFileStorage(t)
	mgr := NewIndexManager(storage, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(tmpDir, "app.go"), goSource("AppV2"), 0644); err != nil {
		t.Fatalf("failed to rewrite app.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "app_test.go"), goSource("AppTestV2"), 0644); err != nil {
		t.Fatalf("failed to rewrite app_test.go: %v", err)
	}
	gitRun(t, tmpDir, "add", "-A")
	gitRun(t, tmpDir, "commit", "-m", "second")

	result, err := mgr.UpdateGit(ctx, tmpDir, "HEAD~1", UpdateOptions{ExcludeTests: true})
	if err != nil {
		t.Fatalf("UpdateGit() error = %v", err)
	}
	if result.FilesUpdated != 1 {
		t.Errorf("FilesUpdated = %d, want 1 (the _test.go file must be excluded)", result.FilesUpdated)
	}

	chunks, err := storage.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	for _, c := range chunks {
		if strings.HasSuffix(c.FilePath, "_test.go") {
			t.Errorf("an excluded test file was indexed: %s", c.FilePath)
		}
	}
	if len(chunks) != 1 || chunks[0].Name != "AppV2" {
		t.Errorf("indexed chunks = %v, want only AppV2", chunks)
	}
}

// TestIndexManager_UpdateGit_NotARepo pins that a non-repository path fails with a
// wrapped git error instead of silently reporting an empty update.
func TestIndexManager_UpdateGit_NotARepo(t *testing.T) {
	storage := newFileStorage(t)
	mgr := NewIndexManager(storage, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())

	_, err := mgr.UpdateGit(context.Background(), t.TempDir(), "HEAD~1", UpdateOptions{})
	if err == nil {
		t.Fatal("UpdateGit() on a non-repository should fail")
	}
	if !strings.Contains(err.Error(), "failed to get git changes") {
		t.Errorf("error = %v, want it wrapped as a git-changes failure", err)
	}
}

// TestIndexManager_FileMatchesFilters pins the single-file filter used by UpdateGit:
// excludes win over includes, a bare directory name excludes any file beneath it, a
// doublestar pattern matches on the relative path, and an unknown extension is rejected
// because no chunker can handle it.
func TestIndexManager_FileMatchesFilters(t *testing.T) {
	mgr := NewIndexManager(newFileStorage(t), &trackingEmbedder{embedding: []float32{0, 0, 0, 0}}, goFactory())
	root := "/repo"

	tests := []struct {
		name         string
		abs          string
		includes     []string
		excludes     []string
		excludeTests bool
		want         bool
	}{
		{"plain go file", "/repo/pkg/app.go", nil, nil, false, true},
		{"unknown extension", "/repo/README.md", nil, nil, false, false},
		{"excluded by bare directory name", "/repo/vendor/lib.go", nil, []string{"vendor"}, false, false},
		{"excluded by glob on basename", "/repo/pkg/app.go", nil, []string{"app.*"}, false, false},
		{"excluded by doublestar path", "/repo/internal/gen/api.go", nil, []string{"**/gen/**"}, false, false},
		{"test file excluded by ExcludeTests", "/repo/pkg/app_test.go", nil, nil, true, false},
		{"test file kept without ExcludeTests", "/repo/pkg/app_test.go", nil, nil, false, true},
		{"include glob matches", "/repo/pkg/app.go", []string{"*.go"}, nil, false, true},
		{"include glob does not match", "/repo/pkg/app.go", []string{"*.py"}, nil, false, false},
		{"include doublestar matches path", "/repo/pkg/deep/app.go", []string{"pkg/**/*.go"}, nil, false, true},
		{"exclude beats include", "/repo/pkg/app.go", []string{"*.go"}, []string{"*.go"}, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mgr.fileMatchesFilters(root, tt.abs, tt.includes, tt.excludes, tt.excludeTests)
			if got != tt.want {
				t.Errorf("fileMatchesFilters(%s) = %v, want %v", tt.abs, got, tt.want)
			}
		})
	}
}

// TestIndexManager_CollectFiles pins the directory walk: it returns only files a chunker
// can handle and honours bare-name, glob, doublestar and ExcludeTests exclusions.
func TestIndexManager_CollectFiles(t *testing.T) {
	tmpDir := t.TempDir()
	files := []string{
		"app.go",
		"app_test.go",
		"README.md",
		filepath.Join("vendor", "lib.go"),
		filepath.Join("internal", "deep", "util.go"),
		filepath.Join("testdata", "fixture.go"),
	}
	for _, f := range files {
		full := filepath.Join(tmpDir, f)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("failed to create dir for %s: %v", f, err)
		}
		if err := os.WriteFile(full, goSource("Fn"), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", f, err)
		}
	}

	mgr := NewIndexManager(newFileStorage(t), &trackingEmbedder{embedding: []float32{0, 0, 0, 0}}, goFactory())

	rel := func(collected []string) []string {
		out := make([]string, len(collected))
		for i, c := range collected {
			r, err := filepath.Rel(tmpDir, c)
			if err != nil {
				t.Fatalf("Rel() error = %v", err)
			}
			out[i] = filepath.ToSlash(r)
		}
		sort.Strings(out)
		return out
	}

	all, err := mgr.collectFiles(tmpDir, nil, nil, false)
	if err != nil {
		t.Fatalf("collectFiles() error = %v", err)
	}
	got := rel(all)
	want := []string{"app.go", "app_test.go", "internal/deep/util.go", "testdata/fixture.go", "vendor/lib.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("collectFiles() = %v, want %v (README.md has no chunker)", got, want)
	}

	filtered, err := mgr.collectFiles(tmpDir, nil, []string{"vendor"}, true)
	if err != nil {
		t.Fatalf("collectFiles() error = %v", err)
	}
	got = rel(filtered)
	want = []string{"app.go", "internal/deep/util.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("collectFiles(exclude vendor, ExcludeTests) = %v, want %v", got, want)
	}

	deep, err := mgr.collectFiles(tmpDir, nil, []string{"**/deep/**"}, false)
	if err != nil {
		t.Fatalf("collectFiles() error = %v", err)
	}
	for _, f := range rel(deep) {
		if strings.Contains(f, "deep/") {
			t.Errorf("doublestar exclude did not drop %s", f)
		}
	}

	only, err := mgr.collectFiles(tmpDir, []string{"util.go"}, nil, false)
	if err != nil {
		t.Fatalf("collectFiles() error = %v", err)
	}
	got = rel(only)
	if len(got) != 1 || got[0] != "internal/deep/util.go" {
		t.Errorf("collectFiles(include util.go) = %v, want just internal/deep/util.go", got)
	}
}

// TestIndexManager_UnreadableFileReportsProgress defends against an unreadable file
// stalling the progress display: the count it reports skips that file's number, so a
// run whose last file is unreadable never reaches Total. Both the per-file and the
// batched path must report the file as Skipped.
func TestIndexManager_UnreadableFileReportsProgress(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a file with no permissions")
	}
	dir := t.TempDir()
	for _, name := range []string{"one.go", "two.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), goSource(strings.ToUpper(name[:1])+name[1:3]), 0644); err != nil {
			t.Fatal(err)
		}
	}
	unreadable := filepath.Join(dir, "two.go")
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(unreadable, 0644) })

	for _, path := range []struct {
		name string
		opts IndexOptions
	}{
		{"per-file", IndexOptions{}},
		{"batched", IndexOptions{EmbedBatchSize: 100}},
	} {
		t.Run(path.name, func(t *testing.T) {
			var events []ProgressEvent
			opts := path.opts
			opts.OnProgress = func(e ProgressEvent) { events = append(events, e) }

			mgr := NewIndexManager(newFileStorage(t), &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())
			result, err := mgr.Index(context.Background(), dir, opts)
			if err != nil {
				t.Fatalf("Index() error = %v", err)
			}
			if result.FilesSkipped != 1 {
				t.Fatalf("FilesSkipped = %d, want 1; the test needs two.go to be unreadable", result.FilesSkipped)
			}

			var got *ProgressEvent
			for i := range events {
				if events[i].FilePath == unreadable {
					got = &events[i]
				}
			}
			if got == nil {
				t.Fatalf("no progress event for the unreadable file; events = %+v", events)
			}
			if !got.Skipped || got.Total != 2 {
				t.Errorf("unreadable file event = %+v, want Skipped with Total 2", *got)
			}
		})
	}
}

// hashErrStorage wraps a real storage whose GetFileHash always fails.
type hashErrStorage struct {
	Storage
	err error
}

func (s *hashErrStorage) GetFileHash(ctx context.Context, filePath string) (string, error) {
	return "", s.err
}

// TestIndexManager_FileHashReadFailureCountsAsFailed defends against a store that cannot
// say whether a file is indexed being read as "needs indexing": the file was re-indexed
// and counted processed, so the store failure vanished. It must be counted as failed and
// named, on both Index paths and both Update paths, and not re-indexed by a full-scan
// Update either.
func TestIndexManager_FileHashReadFailureCountsAsFailed(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "one.go")
	if err := os.WriteFile(file, goSource("One"), 0644); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("hash lookup failed")

	newMgr := func(t *testing.T) (*IndexManager, *SQLiteStorage) {
		inner := newFileStorage(t)
		return NewIndexManager(&hashErrStorage{Storage: inner, err: boom}, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory()), inner
	}
	assertNotIndexed := func(t *testing.T, inner *SQLiteStorage) {
		t.Helper()
		indexed, err := inner.ListIndexedFiles(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if indexed[file] {
			t.Errorf("%s was indexed although its stored hash could not be read", file)
		}
	}

	for _, path := range []struct {
		name string
		opts IndexOptions
	}{
		{"per-file", IndexOptions{}},
		{"batched", IndexOptions{EmbedBatchSize: 100}},
	} {
		t.Run(path.name, func(t *testing.T) {
			mgr, inner := newMgr(t)
			result, err := mgr.Index(context.Background(), dir, path.opts)
			if err != nil {
				t.Fatalf("Index() error = %v", err)
			}
			if result.FilesProcessed != 0 || result.FilesSkipped != 1 {
				t.Errorf("FilesProcessed = %d, FilesSkipped = %d; want 0 and 1", result.FilesProcessed, result.FilesSkipped)
			}
			if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], file) || !strings.Contains(result.Errors[0], boom.Error()) {
				t.Errorf("Errors = %q, want one naming %s and the store failure", result.Errors, file)
			}
			assertNotIndexed(t, inner)
		})
	}

	t.Run("full-scan update", func(t *testing.T) {
		mgr, inner := newMgr(t)
		result, err := mgr.Update(context.Background(), dir, UpdateOptions{Domain: "code"})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if result.FilesUpdated != 0 || result.FilesSkipped != 1 {
			t.Errorf("FilesUpdated = %d, FilesSkipped = %d; want 0 and 1", result.FilesUpdated, result.FilesSkipped)
		}
		if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], file) || !strings.Contains(result.Errors[0], boom.Error()) {
			t.Errorf("Errors = %q, want one naming %s and the store failure", result.Errors, file)
		}
		assertNotIndexed(t, inner)
	})

	t.Run("git update", func(t *testing.T) {
		repo := t.TempDir()
		gitRun(t, repo, "init")
		commitGoFile(t, repo, "base.go", "Base")
		commitGoFile(t, repo, "changed.go", "Changed")
		changed := filepath.Join(repo, "changed.go")

		inner := newFileStorage(t)
		mgr := NewIndexManager(&hashErrStorage{Storage: inner, err: boom}, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())
		result, err := mgr.UpdateGit(context.Background(), repo, "HEAD~1", UpdateOptions{Domain: "code"})
		if err != nil {
			t.Fatalf("UpdateGit() error = %v", err)
		}
		if result.FilesUpdated != 0 || result.FilesSkipped != 1 {
			t.Errorf("FilesUpdated = %d, FilesSkipped = %d; want 0 and 1", result.FilesUpdated, result.FilesSkipped)
		}
		if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], changed) || !strings.Contains(result.Errors[0], boom.Error()) {
			t.Errorf("Errors = %q, want one naming %s and the store failure", result.Errors, changed)
		}
	})
}

// TestIndexManager_LastIndexWriteFailureMessage defends against Index's error claiming
// "index updated" when it is returned: the caller reads that as success with a footnote.
func TestIndexManager_LastIndexWriteFailureMessage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "one.go"), goSource("One"), 0644); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("disk full")
	storage := &lastIndexStorage{Storage: newFileStorage(t), setErr: boom}
	mgr := NewIndexManager(storage, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())

	_, err := mgr.Index(context.Background(), dir, IndexOptions{})
	if !errors.Is(err, boom) {
		t.Fatalf("Index() error = %v, want the SetLastIndex failure", err)
	}
	if strings.HasPrefix(err.Error(), "index updated") {
		t.Errorf("Index() error = %q, must not begin \"index updated\"", err)
	}
}

// TestIndexManager_UpdateGitUnresolvableRefLeavesLastIndex defends the guard on a ref
// that git diff accepts but that names no commit (a tree). Its resolved commit is empty,
// which matches the empty commit an index made before the first commit records, so
// without the guard the update would claim the index is at HEAD when the changes before
// that tree were never applied.
func TestIndexManager_UpdateGitUnresolvableRefLeavesLastIndex(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")
	commitGoFile(t, repo, "two.go", "Two")
	ctx := context.Background()

	storage := newFileStorage(t)
	mgr := NewIndexManager(storage, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())
	planted := LastIndex{Commit: "", IndexedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	if err := storage.SetLastIndex(ctx, planted); err != nil {
		t.Fatal(err)
	}

	result, err := mgr.UpdateGit(ctx, repo, "HEAD~1^{tree}", UpdateOptions{Domain: "code"})
	if err != nil {
		t.Fatalf("UpdateGit() error = %v", err)
	}
	if result.FilesUpdated != 1 {
		t.Fatalf("FilesUpdated = %d, want 1; the test needs git diff to accept the tree ref", result.FilesUpdated)
	}
	if got := mustLastIndex(t, storage); got.Commit != planted.Commit || !got.IndexedAt.Equal(planted.IndexedAt) {
		t.Errorf("after an update from a tree ref: got %+v, want unchanged %+v", *got, planted)
	}
}

// TestIndexManager_UpdateGit_CountsAndNamesAFailedFile is the git-path twin of
// TestIndexManager_Update_CountsAndNamesAFailedFile: a changed file whose embed fails
// was dropped silently, and on this path its old chunks are already deleted.
func TestIndexManager_UpdateGit_CountsAndNamesAFailedFile(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "base.go", "Base")
	commitGoFile(t, repo, "good.go", "Good")
	commitGoFile(t, repo, "bad.go", "Poisoned")

	storage := newFileStorage(t)
	embedder := &poisonEmbedder{trackingEmbedder: trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, marker: "Poisoned"}
	mgr := NewIndexManager(storage, embedder, goFactory())

	result, err := mgr.UpdateGit(context.Background(), repo, "HEAD~2", UpdateOptions{Domain: "code"})
	if err != nil {
		t.Fatalf("UpdateGit() error = %v", err)
	}

	assertOneFailedFile(t, storage, result, filepath.Join(repo, "good.go"), filepath.Join(repo, "bad.go"))
}
