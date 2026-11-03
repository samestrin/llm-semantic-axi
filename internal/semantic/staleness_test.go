package semantic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// countingEmbedder counts every Embed and EmbedBatch call.
type countingEmbedder struct {
	mu    sync.Mutex
	calls int
}

func (e *countingEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return []float32{0.1, 0.2, 0.3, 0.4}, nil
}

func (e *countingEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3, 0.4}
	}
	return out, nil
}

func (e *countingEmbedder) Dimensions() int { return 4 }
func (e *countingEmbedder) Model() string   { return "counting-embedder" }

func (e *countingEmbedder) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// nonGitDir returns a temp directory that git cannot resolve to an enclosing repository.
func nonGitDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	return dir
}

// assertUnknown fails if s reads as fresh in any way: the state, or counts that a
// caller could take to mean "0 behind".
func assertUnknown(t *testing.T, s Staleness, wantReason string) {
	t.Helper()
	if s.State != StalenessUnknown {
		t.Errorf("State = %q, want %q (reason %q)", s.State, StalenessUnknown, s.Reason)
	}
	if s.Fresh() {
		t.Error("Fresh() = true, want false for an unknown staleness")
	}
	if s.Reason != wantReason {
		t.Errorf("Reason = %q, want %q", s.Reason, wantReason)
	}
	if s.CommitsBehind != nil || s.FilesChanged != nil {
		t.Errorf("CommitsBehind = %v, FilesChanged = %v; want both nil, a count would read as measured",
			s.CommitsBehind, s.FilesChanged)
	}
}

// TestCheckStaleness_AddsNoEmbeddingCall is the task's "done when", first half: the
// check is git and storage only. An index built through a counting embedder is checked
// fresh and stale, and the embedder sees no call beyond the ones the index made.
func TestCheckStaleness_AddsNoEmbeddingCall(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")

	storage := newFileStorage(t)
	embedder := &countingEmbedder{}
	ctx := context.Background()
	if _, err := NewIndexManager(storage, embedder, goFactory()).Index(ctx, repo, IndexOptions{}); err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	indexCalls := embedder.Calls()
	if indexCalls == 0 {
		t.Fatal("index made no embedding call; the test cannot tell the check apart")
	}

	if s := CheckStaleness(ctx, storage, repo); s.State != StalenessFresh {
		t.Errorf("right after index: State = %q (reason %q), want fresh", s.State, s.Reason)
	}
	commitGoFile(t, repo, "two.go", "Two")
	if s := CheckStaleness(ctx, storage, repo); s.State != StalenessStale {
		t.Errorf("after a commit: State = %q (reason %q), want stale", s.State, s.Reason)
	}

	if got := embedder.Calls(); got != indexCalls {
		t.Errorf("embedding calls = %d after the check, want %d (the index's own)", got, indexCalls)
	}
}

// TestCheckStaleness_CountsCommitsAndFiles defends the measurement itself: commits since
// the recorded one, and working-tree changes (modified and untracked) on top of them,
// each reported, with the recorded commit and time passed through.
func TestCheckStaleness_CountsCommitsAndFiles(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")
	indexed := gitHead(t, repo)
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	storage := newFileStorage(t)
	ctx := context.Background()
	if err := storage.SetLastIndex(ctx, LastIndex{Commit: indexed, IndexedAt: at}); err != nil {
		t.Fatal(err)
	}

	s := CheckStaleness(ctx, storage, repo)
	if s.State != StalenessFresh || !s.Fresh() {
		t.Fatalf("at the recorded commit: State = %q (reason %q), want fresh", s.State, s.Reason)
	}
	if s.CommitsBehind == nil || *s.CommitsBehind != 0 || s.FilesChanged == nil || *s.FilesChanged != 0 {
		t.Errorf("fresh counts = %v, %v; want 0, 0", s.CommitsBehind, s.FilesChanged)
	}
	if s.IndexedCommit != indexed || !s.IndexedAt.Equal(at) {
		t.Errorf("record = %q at %v, want %q at %v", s.IndexedCommit, s.IndexedAt, indexed, at)
	}

	commitGoFile(t, repo, "two.go", "Two")
	commitGoFile(t, repo, "three.go", "Three")
	if err := os.WriteFile(filepath.Join(repo, "one.go"), goSource("OneEdited"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.go"), goSource("New"), 0644); err != nil {
		t.Fatal(err)
	}

	s = CheckStaleness(ctx, storage, repo)
	if s.State != StalenessStale || s.Fresh() {
		t.Fatalf("State = %q (reason %q), want stale", s.State, s.Reason)
	}
	if s.CommitsBehind == nil || *s.CommitsBehind != 2 {
		t.Errorf("CommitsBehind = %v, want 2", s.CommitsBehind)
	}
	if s.FilesChanged == nil || *s.FilesChanged != 2 {
		t.Errorf("FilesChanged = %v, want 2 (one modified, one untracked)", s.FilesChanged)
	}
}

// TestCheckStaleness_UncommittedEditIsStale defends against "0 commits behind" reading
// as fresh while the working tree differs from what was indexed.
func TestCheckStaleness_UncommittedEditIsStale(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")

	storage := newFileStorage(t)
	ctx := context.Background()
	if err := storage.SetLastIndex(ctx, LastIndex{Commit: gitHead(t, repo), IndexedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "one.go"), goSource("OneEdited"), 0644); err != nil {
		t.Fatal(err)
	}

	s := CheckStaleness(ctx, storage, repo)
	if s.State != StalenessStale {
		t.Errorf("State = %q (reason %q), want stale", s.State, s.Reason)
	}
	if s.CommitsBehind == nil || *s.CommitsBehind != 0 || s.FilesChanged == nil || *s.FilesChanged != 1 {
		t.Errorf("counts = %v, %v; want 0 commits, 1 file", s.CommitsBehind, s.FilesChanged)
	}
}

// TestCheckStaleness_UnknownCases is the task's "done when", second half: every case
// where the index's position cannot be measured reports "unknown", never fresh.
func TestCheckStaleness_UnknownCases(t *testing.T) {
	ctx := context.Background()

	t.Run("missing record", func(t *testing.T) {
		// An index built before the record existed: nil, nil from storage.
		repo := t.TempDir()
		gitRun(t, repo, "init")
		commitGoFile(t, repo, "one.go", "One")
		assertUnknown(t, CheckStaleness(ctx, newFileStorage(t), repo), StalenessReasonNoRecord)
	})

	t.Run("backend cannot record", func(t *testing.T) {
		repo := t.TempDir()
		gitRun(t, repo, "init")
		commitGoFile(t, repo, "one.go", "One")
		storage := &lastIndexStorage{Storage: newFileStorage(t), getErr: ErrLastIndexUnsupported}
		assertUnknown(t, CheckStaleness(ctx, storage, repo), StalenessReasonUnsupported)
	})

	t.Run("record read fails", func(t *testing.T) {
		repo := t.TempDir()
		gitRun(t, repo, "init")
		commitGoFile(t, repo, "one.go", "One")
		storage := &lastIndexStorage{Storage: newFileStorage(t), getErr: errors.New("disk on fire")}
		s := CheckStaleness(ctx, storage, repo)
		assertUnknown(t, s, StalenessReasonReadFailed)
		if !strings.Contains(s.Detail, "disk on fire") {
			t.Errorf("Detail = %q, want the storage error", s.Detail)
		}
	})

	t.Run("non-git directory", func(t *testing.T) {
		dir := nonGitDir(t)
		storage := newFileStorage(t)
		// Recorded outside git: the time is known, the commit is not.
		if err := storage.SetLastIndex(ctx, LastIndex{IndexedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		assertUnknown(t, CheckStaleness(ctx, storage, dir), StalenessReasonNotGit)
	})

	t.Run("non-git directory with a commit recorded", func(t *testing.T) {
		dir := nonGitDir(t)
		storage := newFileStorage(t)
		if err := storage.SetLastIndex(ctx, LastIndex{Commit: strings.Repeat("a", 40), IndexedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		assertUnknown(t, CheckStaleness(ctx, storage, dir), StalenessReasonNotGit)
	})

	t.Run("indexed outside git, now inside", func(t *testing.T) {
		repo := t.TempDir()
		gitRun(t, repo, "init")
		commitGoFile(t, repo, "one.go", "One")
		storage := newFileStorage(t)
		if err := storage.SetLastIndex(ctx, LastIndex{IndexedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		assertUnknown(t, CheckStaleness(ctx, storage, repo), StalenessReasonNoCommit)
	})

	t.Run("repository with no commits", func(t *testing.T) {
		repo := t.TempDir()
		gitRun(t, repo, "init")
		storage := newFileStorage(t)
		if err := storage.SetLastIndex(ctx, LastIndex{Commit: strings.Repeat("a", 40), IndexedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		assertUnknown(t, CheckStaleness(ctx, storage, repo), StalenessReasonNoHead)
	})

	t.Run("recorded commit not in the repository (shallow clone, gc)", func(t *testing.T) {
		repo := t.TempDir()
		gitRun(t, repo, "init")
		commitGoFile(t, repo, "one.go", "One")
		storage := newFileStorage(t)
		if err := storage.SetLastIndex(ctx, LastIndex{Commit: strings.Repeat("a", 40), IndexedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		assertUnknown(t, CheckStaleness(ctx, storage, repo), StalenessReasonCommitUnknown)
	})

	t.Run("recorded commit rewritten away (rebase, amend)", func(t *testing.T) {
		// The old commit still exists in the object store, but HEAD no longer descends
		// from it, so "commits since" has no meaning.
		repo := t.TempDir()
		gitRun(t, repo, "init")
		commitGoFile(t, repo, "one.go", "One")
		commitGoFile(t, repo, "two.go", "Two")
		old := gitHead(t, repo)
		gitRun(t, repo, "commit", "--amend", "-m", "rewritten")

		storage := newFileStorage(t)
		if err := storage.SetLastIndex(ctx, LastIndex{Commit: old, IndexedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		assertUnknown(t, CheckStaleness(ctx, storage, repo), StalenessReasonCommitUnknown)
	})
}

// TestCheckStaleness_IgnoresTheIndexItself defends against the default index location
// (`.index/` inside the repository) making every index read as one file behind: the
// index's own directory is passed as ignored, while a real change next to it still counts.
func TestCheckStaleness_IgnoresTheIndexItself(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")

	indexDir := filepath.Join(repo, ".index")
	storage, err := NewSQLiteStorage(filepath.Join(indexDir, "semantic.db"), 4)
	if err != nil {
		t.Fatalf("NewSQLiteStorage() error = %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	ctx := context.Background()
	if err := storage.SetLastIndex(ctx, LastIndex{Commit: gitHead(t, repo), IndexedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if s := CheckStaleness(ctx, storage, repo); s.State != StalenessStale {
		t.Fatalf("without ignore: State = %q, want stale (the untracked .index/ is a change)", s.State)
	}
	if s := CheckStaleness(ctx, storage, repo, indexDir); s.State != StalenessFresh {
		t.Errorf("ignoring .index: State = %q (files %v), want fresh", s.State, s.FilesChanged)
	}

	if err := os.WriteFile(filepath.Join(repo, "new.go"), goSource("New"), 0644); err != nil {
		t.Fatal(err)
	}
	s := CheckStaleness(ctx, storage, repo, indexDir)
	if s.State != StalenessStale || s.FilesChanged == nil || *s.FilesChanged != 1 {
		t.Errorf("ignoring .index with new.go: State = %q, FilesChanged = %v; want stale, 1", s.State, s.FilesChanged)
	}

	// A path outside the repository is not a reason to drop anything.
	s = CheckStaleness(ctx, storage, repo, t.TempDir())
	if s.FilesChanged == nil || *s.FilesChanged != 2 {
		t.Errorf("ignore outside the repo: FilesChanged = %v, want 2", s.FilesChanged)
	}
}

// TestCheckStaleness_RenameCountsOnce defends the -z parser: a staged rename carries its
// source path as an extra field, which must not be counted as a second change.
func TestCheckStaleness_RenameCountsOnce(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")
	storage := newFileStorage(t)
	ctx := context.Background()
	if err := storage.SetLastIndex(ctx, LastIndex{Commit: gitHead(t, repo), IndexedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "mv", "one.go", "renamed.go")

	s := CheckStaleness(ctx, storage, repo)
	if s.State != StalenessStale || s.FilesChanged == nil || *s.FilesChanged != 1 {
		t.Errorf("State = %q, FilesChanged = %v; want stale, 1", s.State, s.FilesChanged)
	}
}

// TestCheckStaleness_GitUnavailable defends against a missing git binary being reported
// as "not a git repository" (or as fresh): it is a failure to measure.
func TestCheckStaleness_GitUnavailable(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")
	storage := newFileStorage(t)
	ctx := context.Background()
	if err := storage.SetLastIndex(ctx, LastIndex{Commit: gitHead(t, repo), IndexedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")

	s := CheckStaleness(ctx, storage, repo)
	assertUnknown(t, s, StalenessReasonGitFailed)
	if s.Detail == "" {
		t.Error("Detail is empty, want the exec error")
	}
}

// TestCheckStaleness_RejectsNonHashRecord defends against a corrupt or hostile record
// reaching git as an argument (e.g. an option such as "--output=x"): it is unknown.
func TestCheckStaleness_RejectsNonHashRecord(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")
	storage := newFileStorage(t)
	ctx := context.Background()
	for _, rec := range []string{"HEAD", "--output=" + filepath.Join(repo, "x"), gitHead(t, repo)[:12]} {
		if err := storage.SetLastIndex(ctx, LastIndex{Commit: rec, IndexedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		assertUnknown(t, CheckStaleness(ctx, storage, repo), StalenessReasonCommitUnknown)
	}
}

// TestCheckStaleness_GitRefusalIsNotNotGit defends against git refusing a repository it
// can see (safe.directory "dubious ownership", a broken .git) being reported as "not a
// git repository": the directory is in git, git just could not measure it.
func TestCheckStaleness_GitRefusalIsNotNotGit(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")
	storage := newFileStorage(t)
	ctx := context.Background()
	if err := storage.SetLastIndex(ctx, LastIndex{Commit: gitHead(t, repo), IndexedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Corrupt HEAD so every git command in the repository fails, without it ceasing to be one.
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("garbage\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := CheckStaleness(ctx, storage, repo)
	if s.Reason == StalenessReasonNotGit {
		t.Errorf("Reason = %q, want a failure reason: the directory is a repository git refused", s.Reason)
	}
	if s.State != StalenessUnknown {
		t.Errorf("State = %q, want unknown", s.State)
	}
}

// TestCheckStaleness_CancelledContext defends against a cancelled check being blamed on
// the repository (not_git, no_head, commit_unknown): it is a failure to measure.
func TestCheckStaleness_CancelledContext(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")
	// The record reads regardless of ctx, so the cancellation reaches the git commands.
	storage := fixedLastIndexStorage{Storage: newFileStorage(t), li: LastIndex{Commit: gitHead(t, repo), IndexedAt: time.Now()}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := CheckStaleness(ctx, storage, repo)
	if s.State != StalenessUnknown || s.Reason != StalenessReasonGitFailed {
		t.Errorf("State = %q, Reason = %q; want unknown, %q", s.State, s.Reason, StalenessReasonGitFailed)
	}
}

// fixedLastIndexStorage returns li from GetLastIndex without touching the database.
type fixedLastIndexStorage struct {
	Storage
	li LastIndex
}

func (s fixedLastIndexStorage) GetLastIndex(context.Context) (*LastIndex, error) {
	li := s.li
	return &li, nil
}
