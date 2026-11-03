package semantic

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitHead returns the full HEAD commit of the repository at dir.
func gitHead(t *testing.T, dir string) string {
	t.Helper()
	return gitHeadOf(t, dir, "HEAD")
}

// commitGoFile writes name with a function fn into dir and commits it.
func commitGoFile(t *testing.T, dir, name, fn string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), goSource(fn), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", name, err)
	}
	gitRun(t, dir, "add", name)
	gitRun(t, dir, "commit", "-m", "commit "+fn)
}

func mustLastIndex(t *testing.T, storage Storage) *LastIndex {
	t.Helper()
	got, err := storage.GetLastIndex(context.Background())
	if err != nil {
		t.Fatalf("GetLastIndex() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetLastIndex() = nil, want a recorded last index")
	}
	return got
}

// TestSQLiteStorage_LastIndexRoundTrip defends the stored value itself: a fresh index
// reads as "never recorded" (nil, not a zero commit that looks real), a write reads back
// exactly, and a second write replaces the first.
func TestSQLiteStorage_LastIndexRoundTrip(t *testing.T) {
	storage := newFileStorage(t)
	ctx := context.Background()

	got, err := storage.GetLastIndex(ctx)
	if err != nil {
		t.Fatalf("GetLastIndex() on fresh index error = %v", err)
	}
	if got != nil {
		t.Fatalf("GetLastIndex() on fresh index = %+v, want nil", got)
	}

	first := LastIndex{Commit: "aaaa", IndexedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	if err := storage.SetLastIndex(ctx, first); err != nil {
		t.Fatalf("SetLastIndex() error = %v", err)
	}
	if got := mustLastIndex(t, storage); got.Commit != first.Commit || !got.IndexedAt.Equal(first.IndexedAt) {
		t.Errorf("GetLastIndex() = %+v, want %+v", got, first)
	}

	second := LastIndex{Commit: "bbbb", IndexedAt: first.IndexedAt.Add(time.Hour)}
	if err := storage.SetLastIndex(ctx, second); err != nil {
		t.Fatalf("SetLastIndex() second write error = %v", err)
	}
	if got := mustLastIndex(t, storage); got.Commit != second.Commit || !got.IndexedAt.Equal(second.IndexedAt) {
		t.Errorf("GetLastIndex() after overwrite = %+v, want %+v", got, second)
	}
}

// TestSQLiteStorage_ClearDropsLastIndex defends against a cleared index still claiming
// it reflects a commit: after Clear there is nothing indexed, so nothing is recorded.
func TestSQLiteStorage_ClearDropsLastIndex(t *testing.T) {
	storage := newFileStorage(t)
	ctx := context.Background()

	if err := storage.SetLastIndex(ctx, LastIndex{Commit: "aaaa", IndexedAt: time.Now()}); err != nil {
		t.Fatalf("SetLastIndex() error = %v", err)
	}
	if err := storage.Clear(ctx); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	got, err := storage.GetLastIndex(ctx)
	if err != nil {
		t.Fatalf("GetLastIndex() error = %v", err)
	}
	if got != nil {
		t.Errorf("GetLastIndex() after Clear = %+v, want nil", got)
	}
}

// TestSQLiteStorage_LastIndexClosed defends against a closed storage silently reporting
// "never indexed" or accepting a write it cannot keep.
func TestSQLiteStorage_LastIndexClosed(t *testing.T) {
	storage, err := NewSQLiteStorage(filepath.Join(t.TempDir(), "index.db"), 4)
	if err != nil {
		t.Fatalf("NewSQLiteStorage() error = %v", err)
	}
	storage.Close()
	ctx := context.Background()

	if _, err := storage.GetLastIndex(ctx); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("GetLastIndex() on closed storage error = %v, want ErrStorageClosed", err)
	}
	if err := storage.SetLastIndex(ctx, LastIndex{Commit: "aaaa"}); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("SetLastIndex() on closed storage error = %v, want ErrStorageClosed", err)
	}
}

// TestSQLiteStorage_LastIndexCorrupt defends against a mangled row being read as
// "never indexed": it is reported as an error.
func TestSQLiteStorage_LastIndexCorrupt(t *testing.T) {
	storage := newFileStorage(t)
	ctx := context.Background()

	if _, err := storage.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO index_metadata (key, value) VALUES (?, '{not json')`, lastIndexKey); err != nil {
		t.Fatalf("failed to plant corrupt row: %v", err)
	}
	if _, err := storage.GetLastIndex(ctx); err == nil {
		t.Error("GetLastIndex() on corrupt row error = nil, want a parse error")
	}
}

// TestIndexManager_RecordsLastIndex is the task's "done when": the stored commit is read
// back after index, after an `index-update --since` whose ref matches the recorded commit
// (advances to HEAD), after one whose ref does not (left unchanged, time included), and
// after a full-scan update (advances to HEAD). A git update from an unrelated ref must not
// claim the index is at HEAD, because the commits between the recorded one and that ref
// were never applied.
func TestIndexManager_RecordsLastIndex(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")
	c1 := gitHead(t, repo)

	storage := newFileStorage(t)
	mgr := NewIndexManager(storage, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())
	ctx := context.Background()

	// index records HEAD and the time it ran.
	before := time.Now().Add(-time.Second)
	if _, err := mgr.Index(ctx, repo, IndexOptions{}); err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	after := time.Now().Add(time.Second)
	got := mustLastIndex(t, storage)
	if got.Commit != c1 {
		t.Errorf("after index: Commit = %q, want HEAD %q", got.Commit, c1)
	}
	if got.IndexedAt.Before(before) || got.IndexedAt.After(after) {
		t.Errorf("after index: IndexedAt = %v, want between %v and %v", got.IndexedAt, before, after)
	}

	// --since matching the recorded commit advances it to the new HEAD.
	commitGoFile(t, repo, "two.go", "Two")
	c2 := gitHead(t, repo)
	if _, err := mgr.UpdateGit(ctx, repo, "HEAD~1", UpdateOptions{Domain: "code"}); err != nil {
		t.Fatalf("UpdateGit(HEAD~1) error = %v", err)
	}
	got = mustLastIndex(t, storage)
	if got.Commit != c2 {
		t.Errorf("after matching --since: Commit = %q, want HEAD %q", got.Commit, c2)
	}
	matched := *got

	// --since not matching the recorded commit leaves the value, time included, unchanged.
	commitGoFile(t, repo, "three.go", "Three")
	commitGoFile(t, repo, "four.go", "Four")
	if _, err := mgr.UpdateGit(ctx, repo, "HEAD~1", UpdateOptions{Domain: "code"}); err != nil {
		t.Fatalf("UpdateGit(HEAD~1) error = %v", err)
	}
	got = mustLastIndex(t, storage)
	if got.Commit != matched.Commit || !got.IndexedAt.Equal(matched.IndexedAt) {
		t.Errorf("after non-matching --since: got %+v, want unchanged %+v", *got, matched)
	}

	// A full-scan update covers every file, so it records HEAD.
	c4 := gitHead(t, repo)
	if _, err := mgr.Update(ctx, repo, UpdateOptions{Domain: "code"}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got := mustLastIndex(t, storage); got.Commit != c4 {
		t.Errorf("after full-scan update: Commit = %q, want HEAD %q", got.Commit, c4)
	}
}

// TestIndexManager_UpdateGitWithNothingRecorded defends against a git update on an index
// that never recorded a commit (built before this was tracked) claiming to be at HEAD.
func TestIndexManager_UpdateGitWithNothingRecorded(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")
	commitGoFile(t, repo, "two.go", "Two")

	storage := newFileStorage(t)
	mgr := NewIndexManager(storage, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())
	if _, err := mgr.UpdateGit(context.Background(), repo, "HEAD~1", UpdateOptions{Domain: "code"}); err != nil {
		t.Fatalf("UpdateGit() error = %v", err)
	}
	got, err := storage.GetLastIndex(context.Background())
	if err != nil {
		t.Fatalf("GetLastIndex() error = %v", err)
	}
	if got != nil {
		t.Errorf("GetLastIndex() = %+v, want nil: nothing was recorded to match --since", got)
	}
}

// TestIndexManager_RecordsLastIndexOutsideGit defends the non-git case: the time of the
// index is still recorded, with an empty commit rather than an invented one.
func TestIndexManager_RecordsLastIndexOutsideGit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "one.go"), goSource("One"), 0644); err != nil {
		t.Fatal(err)
	}
	// Keep git from walking up into an enclosing repository.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))

	storage := newFileStorage(t)
	mgr := NewIndexManager(storage, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())
	if _, err := mgr.Index(context.Background(), dir, IndexOptions{}); err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	got := mustLastIndex(t, storage)
	if got.Commit != "" {
		t.Errorf("Commit = %q, want empty outside git", got.Commit)
	}
	if got.IndexedAt.IsZero() {
		t.Error("IndexedAt is zero, want the time of the index")
	}
}

// lastIndexStorage wraps a real storage and overrides the last-index methods.
type lastIndexStorage struct {
	Storage
	getErr, setErr error
	sets           int
}

func (s *lastIndexStorage) GetLastIndex(ctx context.Context) (*LastIndex, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.Storage.GetLastIndex(ctx)
}

func (s *lastIndexStorage) SetLastIndex(ctx context.Context, li LastIndex) error {
	s.sets++
	if s.setErr != nil {
		return s.setErr
	}
	return s.Storage.SetLastIndex(ctx, li)
}

// TestIndexManager_LastIndexWriteFailureIsReported defends against an index that could
// not record its commit reporting plain success: the staleness check would then trust an
// older value.
func TestIndexManager_LastIndexWriteFailureIsReported(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")
	commitGoFile(t, repo, "two.go", "Two")
	ctx := context.Background()
	boom := errors.New("disk full")

	storage := &lastIndexStorage{Storage: newFileStorage(t), setErr: boom}
	mgr := NewIndexManager(storage, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())

	if _, err := mgr.Index(ctx, repo, IndexOptions{}); !errors.Is(err, boom) {
		t.Errorf("Index() error = %v, want the SetLastIndex failure", err)
	}
	if _, err := mgr.Update(ctx, repo, UpdateOptions{Domain: "code"}); !errors.Is(err, boom) {
		t.Errorf("Update() error = %v, want the SetLastIndex failure", err)
	}

	// UpdateGit only writes when the recorded commit matches, so plant one that does.
	if err := storage.Storage.SetLastIndex(ctx, LastIndex{Commit: gitHeadOf(t, repo, "HEAD~1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.UpdateGit(ctx, repo, "HEAD~1", UpdateOptions{Domain: "code"}); !errors.Is(err, boom) {
		t.Errorf("UpdateGit() error = %v, want the SetLastIndex failure", err)
	}
}

// TestIndexManager_UpdateGitReadFailureIsReported defends against a failed read of the
// recorded commit being treated as "does not match" and passed over in silence.
func TestIndexManager_UpdateGitReadFailureIsReported(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")
	commitGoFile(t, repo, "two.go", "Two")
	boom := errors.New("read failed")

	storage := &lastIndexStorage{Storage: newFileStorage(t), getErr: boom}
	mgr := NewIndexManager(storage, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())
	if _, err := mgr.UpdateGit(context.Background(), repo, "HEAD~1", UpdateOptions{Domain: "code"}); !errors.Is(err, boom) {
		t.Errorf("UpdateGit() error = %v, want the GetLastIndex failure", err)
	}
}

// TestIndexManager_LastIndexUnsupportedIsNotAFailure defends backends that cannot store
// the record (Qdrant): indexing still succeeds, and the record stays unreadable rather
// than faked, so the staleness check can say it does not know.
func TestIndexManager_LastIndexUnsupportedIsNotAFailure(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")
	commitGoFile(t, repo, "two.go", "Two")
	ctx := context.Background()

	storage := &lastIndexStorage{Storage: newFileStorage(t), getErr: ErrLastIndexUnsupported, setErr: ErrLastIndexUnsupported}
	mgr := NewIndexManager(storage, &trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, goFactory())

	if _, err := mgr.Index(ctx, repo, IndexOptions{}); err != nil {
		t.Errorf("Index() error = %v, want nil when the backend cannot record", err)
	}
	if _, err := mgr.Update(ctx, repo, UpdateOptions{Domain: "code"}); err != nil {
		t.Errorf("Update() error = %v, want nil when the backend cannot record", err)
	}
	if _, err := mgr.UpdateGit(ctx, repo, "HEAD~1", UpdateOptions{Domain: "code"}); err != nil {
		t.Errorf("UpdateGit() error = %v, want nil when the backend cannot record", err)
	}
}

// gitHeadOf resolves rev in the repository at dir.
func gitHeadOf(t *testing.T, dir, rev string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", rev)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse %s failed: %v", rev, err)
	}
	return strings.TrimSpace(string(out))
}

// failingEmbedder embeds like trackingEmbedder but fails any batch containing marker,
// so one file in a run can fail to embed while the rest succeed.
type failingEmbedder struct {
	trackingEmbedder
	marker string
}

func (f *failingEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	for _, text := range texts {
		if strings.Contains(text, f.marker) {
			return nil, errors.New("embedding endpoint rejected the batch")
		}
	}
	return f.trackingEmbedder.EmbedBatch(ctx, texts)
}

// TestIndexManager_FileFailureLeavesLastIndex defends against an index with files that
// failed to embed recording HEAD: a later search would then report the index fresh when
// part of it is missing. Each of Index, the full-scan Update and UpdateGit's advance must
// leave the previous record, time included, unchanged when any file failed.
func TestIndexManager_FileFailureLeavesLastIndex(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")
	ctx := context.Background()

	storage := newFileStorage(t)
	embedder := &failingEmbedder{trackingEmbedder: trackingEmbedder{embedding: []float32{0.1, 0.2, 0.3, 0.4}}, marker: "Broken"}
	mgr := NewIndexManager(storage, embedder, goFactory())

	planted := LastIndex{Commit: "planted", IndexedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	assertUnchanged := func(step string) {
		t.Helper()
		got := mustLastIndex(t, storage)
		if got.Commit != planted.Commit || !got.IndexedAt.Equal(planted.IndexedAt) {
			t.Errorf("after %s with a failed file: got %+v, want unchanged %+v", step, *got, planted)
		}
	}
	plant := func() {
		t.Helper()
		if err := storage.SetLastIndex(ctx, planted); err != nil {
			t.Fatal(err)
		}
	}

	commitGoFile(t, repo, "broken.go", "Broken")

	// Index, per-file path.
	plant()
	result, err := mgr.Index(ctx, repo, IndexOptions{})
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if len(result.Errors) == 0 {
		t.Fatal("Index() reported no errors; the test needs broken.go to fail")
	}
	assertUnchanged("index")

	// Index, cross-file batching path (an embed failure there aborts the run).
	plant()
	if _, err := mgr.Index(ctx, repo, IndexOptions{EmbedBatchSize: 100}); err == nil {
		t.Fatal("Index(EmbedBatchSize) error = nil; the test needs broken.go to fail")
	}
	assertUnchanged("batched index")

	// Full-scan Update: broken.go was never stored, so it is retried and fails again.
	plant()
	if _, err := mgr.Update(ctx, repo, UpdateOptions{Domain: "code"}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	assertUnchanged("full-scan update")

	// UpdateGit from a ref matching the recorded commit would advance it to HEAD.
	planted.Commit = gitHeadOf(t, repo, "HEAD~1")
	plant()
	if _, err := mgr.UpdateGit(ctx, repo, "HEAD~1", UpdateOptions{Domain: "code"}); err != nil {
		t.Fatalf("UpdateGit() error = %v", err)
	}
	assertUnchanged("git update")

	// Once nothing fails, each records HEAD again, so the guard is not blocking every write.
	embedder.marker = "\x00never"
	if _, err := mgr.Update(ctx, repo, UpdateOptions{Domain: "code"}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got := mustLastIndex(t, storage); got.Commit != gitHead(t, repo) {
		t.Errorf("after a clean update: Commit = %q, want HEAD %q", got.Commit, gitHead(t, repo))
	}
}
