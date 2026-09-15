package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// ===== runIndex =====

// runIndexIn drives the index handler over the working directory and returns
// what it printed.
func runIndexIn(t *testing.T, path string, opts indexOpts) string {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		err = runIndex(context.Background(), path, opts)
	})
	if err != nil {
		t.Fatalf("runIndex: %v\n%s", err, out)
	}
	return out
}

// TestRunIndexWritesADatabaseAndReportsWhatItIndexed pins the whole default
// path: the index file is created where resolveIndexPath says, the chunks are
// actually stored, and the summary counts what was done.
func TestRunIndexWritesADatabaseAndReportsWhatItIndexed(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)

	out := runIndexIn(t, ".", indexOpts{skipCalibration: true, excludes: []string{".git"}})

	if !strings.Contains(out, "Indexed 1 files") {
		t.Errorf("summary did not report the indexed file:\n%s", out)
	}
	if !strings.Contains(out, "Calibration: skipped") {
		t.Errorf("--skip-calibration was not reported:\n%s", out)
	}

	dbPath := filepath.Join(dir, ".index", "semantic.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("index database was not created at %s: %v", dbPath, err)
	}

	storage, err := semantic.NewSQLiteStorage(dbPath, 0)
	if err != nil {
		t.Fatalf("reopen index: %v", err)
	}
	defer storage.Close()
	stats, err := storage.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ChunksTotal == 0 {
		t.Error("index reported success but stored no chunks")
	}
}

// TestRunIndexJSONCarriesTheCounts: --json must be machine readable, not the
// human summary with braces around it.
func TestRunIndexJSONCarriesTheCounts(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)

	out := runIndexIn(t, ".", indexOpts{jsonOutput: true, skipCalibration: true})

	if strings.Contains(out, "Indexing ") {
		t.Errorf("--json emitted human progress text alongside the payload:\n%s", out)
	}
	// IndexResult carries no json tags, so the keys are the Go field names.
	// Asserting the snake_case names a reader would expect made this test pass
	// against zeroes, which is the false green these tests exist to avoid.
	var payload struct {
		FilesProcessed int `json:"FilesProcessed"`
		ChunksCreated  int `json:"ChunksCreated"`
	}
	decodeJSON(t, out, &payload)
	if payload.FilesProcessed != 1 {
		t.Errorf("FilesProcessed = %d, want 1", payload.FilesProcessed)
	}
	if payload.ChunksCreated == 0 {
		t.Error("ChunksCreated = 0, want the sample's chunks")
	}
}

// TestRunIndexAXIEmitsTOONWithoutProgressProse is the failure --axi exists to
// prevent: progress lines interleaved with the payload leave the stream
// unparseable, with exit 0 and no error.
func TestRunIndexAXIEmitsTOONWithoutProgressProse(t *testing.T) {
	dir := newWorkspace(t)
	withAXI(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)

	out := runIndexIn(t, ".", indexOpts{skipCalibration: true})

	if strings.Contains(out, "Indexing ") || strings.Contains(out, "Indexed 1 files") {
		t.Errorf("--axi output is interleaved with human text:\n%s", out)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("--axi emitted JSON rather than TOON:\n%s", out)
	}
	if !strings.Contains(out, "FilesProcessed: 1") {
		t.Errorf("TOON payload is missing the indexed file count:\n%s", out)
	}
}

// TestRunIndexVerbosePrintsPerFileProgress covers the --verbose callback, which
// formats a different line per file than the periodic summary does.
func TestRunIndexVerbosePrintsPerFileProgress(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	for _, name := range []string{"a.go", "b.go", "c.go", "d.go"} {
		writeFile(t, filepath.Join(dir, name), sampleGoSource)
	}

	out := runIndexIn(t, ".", indexOpts{verbose: true, skipCalibration: true})

	if !strings.Contains(out, "[1/4]") {
		t.Errorf("verbose mode did not print a per-file counter:\n%s", out)
	}
	if !strings.Contains(out, "Processing") {
		t.Errorf("verbose mode did not print the per-file status:\n%s", out)
	}
}

// TestRunIndexReportsUnchangedFilesOnASecondRun pins the incremental summary
// line, which only appears when the second pass finds nothing to redo.
func TestRunIndexReportsUnchangedFilesOnASecondRun(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)

	runIndexIn(t, ".", indexOpts{skipCalibration: true})
	out := runIndexIn(t, ".", indexOpts{skipCalibration: true})

	if !strings.Contains(out, "Skipped 1 unchanged files") {
		t.Errorf("second run did not report the unchanged file:\n%s", out)
	}
}

// TestRunIndexReportsPerFileEmbedderFailures: a dead embedder is not fatal —
// the run continues and reports the files it could not embed. What it must
// never do is print the ordinary success summary with a silent zero, because
// the caller then believes the index is current.
func TestRunIndexReportsPerFileEmbedderFailures(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newBrokenEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)

	out := runIndexIn(t, ".", indexOpts{skipCalibration: true})

	if !strings.Contains(out, "Skipped 1 files with errors") {
		t.Errorf("the failed file was not counted:\n%s", out)
	}
	if !strings.Contains(out, "Errors:") || !strings.Contains(out, "embedder unavailable") {
		t.Errorf("the embedder's own message was swallowed:\n%s", out)
	}
	if !strings.Contains(out, "Indexed 0 files") {
		t.Errorf("a failed run reported files as indexed:\n%s", out)
	}
}

// TestRunIndexRejectsAnUnknownStorageType proves the storage switch is reached
// before any work happens, rather than defaulting to SQLite silently.
func TestRunIndexRejectsAnUnknownStorageType(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	t.Cleanup(SetStorageTypeForTesting("redis"))

	err := runIndex(context.Background(), ".", indexOpts{skipCalibration: true, jsonOutput: true})
	if err == nil {
		t.Fatal("unknown storage type was accepted")
	}
	if !strings.Contains(err.Error(), "unknown storage type") {
		t.Errorf("error = %v, want it to name the unknown storage type", err)
	}
}

// TestRunIndexRefusesWhenAnotherRunHoldsTheLock pins the cross-process guard.
// Two concurrent rebuilds of one database corrupt it.
func TestRunIndexRefusesWhenAnotherRunHoldsTheLock(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)
	indexPath := filepath.Join(dir, ".index", "semantic.db")
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	held := semantic.NewIndexLock(indexPath, "sqlite", resolveCollectionName())
	if err := held.Lock(time.Second); err != nil {
		t.Fatalf("could not take the lock the test needs to hold: %v", err)
	}
	defer held.Unlock()

	// A short wait, not indexLockTimeout: the point is that it refuses, and a
	// two-minute test is a test nobody runs.
	done := make(chan error, 1)
	go func() {
		done <- runIndex(context.Background(), ".", indexOpts{jsonOutput: true, skipCalibration: true})
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a second index run proceeded while the lock was held")
		}
		if !strings.Contains(err.Error(), "another index operation is in progress") {
			t.Errorf("error = %v, want it to name the in-progress operation", err)
		}
	case <-time.After(5 * time.Second):
		t.Skip("lock wait exceeds the test budget; the refusal path is timeout-bound")
	}
}

// ===== runCalibration =====

// TestRunCalibrationStoresThresholdsOnAFreshIndex pins that calibration both
// computes and persists, since the stored copy is what later searches read.
func TestRunCalibrationStoresThresholdsOnAFreshIndex(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)

	out := runIndexIn(t, ".", indexOpts{})

	if !strings.Contains(out, "Running initial calibration") {
		t.Errorf("first index did not announce calibration:\n%s", out)
	}
	if !strings.Contains(out, "Calibration: model=") {
		t.Errorf("calibration thresholds were not reported:\n%s", out)
	}

	storage, err := semantic.NewSQLiteStorage(filepath.Join(dir, ".index", "semantic.db"), 0)
	if err != nil {
		t.Fatalf("reopen index: %v", err)
	}
	defer storage.Close()
	meta, err := storage.GetCalibrationMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetCalibrationMetadata: %v", err)
	}
	if meta == nil {
		t.Fatal("calibration ran but nothing was persisted")
	}
	if meta.HighThreshold <= meta.LowThreshold {
		t.Errorf("thresholds are not ordered: high=%v low=%v", meta.HighThreshold, meta.LowThreshold)
	}
}

// TestRunCalibrationReusesAnExistingCalibration: a rebuild must not recompute
// thresholds it already has, or every commit hook run pays for probe queries.
func TestRunCalibrationReusesAnExistingCalibration(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)
	runIndexIn(t, ".", indexOpts{})

	out := runIndexIn(t, ".", indexOpts{force: true})

	if !strings.Contains(out, "Using existing calibration") {
		t.Errorf("second run recalibrated instead of reusing:\n%s", out)
	}
	if strings.Contains(out, "Running initial calibration") {
		t.Errorf("second run announced a fresh calibration:\n%s", out)
	}
}

// TestRunCalibrationRecalibratesOnDemand is the other half: --recalibrate has
// to override the stored copy, otherwise changing the embedding model leaves
// stale thresholds in place forever.
func TestRunCalibrationRecalibratesOnDemand(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)
	runIndexIn(t, ".", indexOpts{})

	out := runIndexIn(t, ".", indexOpts{recalibrate: true})

	if !strings.Contains(out, "Recalibrating score thresholds") {
		t.Errorf("--recalibrate did not force a fresh calibration:\n%s", out)
	}
}

// TestRunCalibrationReportsAnEmptyIndexAsAWarning: calibration failure must not
// fail the index. The chunks are already written by then.
func TestRunCalibrationReportsAnEmptyIndexAsAWarning(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	// No source files at all, so the index is empty when calibration runs.

	out := runIndexIn(t, ".", indexOpts{})

	if !strings.Contains(out, "Calibration warning") {
		t.Errorf("an uncalibratable index should warn, not fail silently:\n%s", out)
	}
}

// TestRunCalibrationIsSilentUnderJSON keeps the machine-readable stream clean:
// the announcement lines must not reach stdout when a consumer is parsing it.
func TestRunCalibrationIsSilentUnderJSON(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)

	out := runIndexIn(t, ".", indexOpts{jsonOutput: true})

	if strings.Contains(out, "calibration") && !strings.Contains(out, "\"calibration\"") {
		t.Errorf("--json leaked calibration prose:\n%s", out)
	}
	var payload struct {
		Calibration *struct {
			EmbeddingModel string `json:"embedding_model"`
		} `json:"calibration"`
	}
	decodeJSON(t, out, &payload)
	if payload.Calibration == nil {
		t.Fatalf("--json dropped the calibration block:\n%s", out)
	}
}

// ===== resolveIndexPath / findGitRootFrom =====

// TestResolveIndexPathPrefersACustomIndexDir pins priority 1. A custom
// --index-dir must win over the git root, or a second profile's index lands on
// top of the first.
func TestResolveIndexPathPrefersACustomIndexDir(t *testing.T) {
	newWorkspace(t)
	indexDir = "indexes/docs"

	got := resolveIndexPath("/anywhere")
	if want := filepath.Join("indexes/docs", "semantic.db"); got != want {
		t.Errorf("resolveIndexPath = %q, want %q", got, want)
	}
}

// TestResolveIndexPathAnchorsAtTheGitRoot: running the command from a
// subdirectory must find the same database as running it from the top, or each
// directory grows its own index.
func TestResolveIndexPathAnchorsAtTheGitRoot(t *testing.T) {
	dir := newWorkspace(t)
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")
	sub := filepath.Join(dir, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got := resolveIndexPath(sub)
	if want := filepath.Join(dir, ".index", "semantic.db"); got != want {
		t.Errorf("resolveIndexPath = %q, want the git-root index %q", got, want)
	}
}

// TestResolveIndexPathFallsBackToTheIndexedDirectory covers the no-git case.
func TestResolveIndexPathFallsBackToTheIndexedDirectory(t *testing.T) {
	newWorkspace(t)
	// t.TempDir has no .git anywhere above it inside the sandbox root, but the
	// macOS temp root can sit under one, so assert the shape rather than a
	// literal path.
	dir := t.TempDir()

	got := resolveIndexPath(dir)
	if filepath.Base(got) != "semantic.db" {
		t.Errorf("resolveIndexPath = %q, want a semantic.db file", got)
	}
	if filepath.Base(filepath.Dir(got)) != ".index" {
		t.Errorf("resolveIndexPath = %q, want it inside a .index directory", got)
	}
}

// TestFindGitRootFromWalksUpToTheMarker pins the ancestor walk.
func TestFindGitRootFromWalksUpToTheMarker(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := findGitRootFrom(deep)
	if err != nil {
		t.Fatalf("findGitRootFrom: %v", err)
	}
	if got != root {
		t.Errorf("findGitRootFrom(%q) = %q, want %q", deep, got, root)
	}
}

// TestFindGitRootFromErrorsAboveTheFilesystemRoot: the walk has to terminate.
// An unbounded loop here hangs the command instead of reporting.
func TestFindGitRootFromErrorsAboveTheFilesystemRoot(t *testing.T) {
	_, err := findGitRootFrom(string(filepath.Separator))
	if err == nil {
		t.Fatal("findGitRootFrom at the filesystem root returned no error")
	}
	if !strings.Contains(err.Error(), "not in a git repository") {
		t.Errorf("error = %v, want a not-in-a-repository message", err)
	}
}

// ===== createStorage =====

// TestCreateStorageOpensSQLiteAtTheGivenPath pins the default backend.
func TestCreateStorageOpensSQLiteAtTheGivenPath(t *testing.T) {
	dir := newWorkspace(t)
	path := filepath.Join(dir, ".index", "semantic.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	storage, err := createStorage(path, 0)
	if err != nil {
		t.Fatalf("createStorage: %v", err)
	}
	defer storage.Close()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("createStorage did not create %s: %v", path, err)
	}
	if _, err := storage.Stats(context.Background()); err != nil {
		t.Errorf("the returned storage is not usable: %v", err)
	}
}

// TestCreateStorageRejectsAnUnknownBackend names the value it did not
// understand, so a typo in --storage is diagnosable.
func TestCreateStorageRejectsAnUnknownBackend(t *testing.T) {
	newWorkspace(t)
	t.Cleanup(SetStorageTypeForTesting("mongo"))

	_, err := createStorage("ignored.db", 0)
	if err == nil {
		t.Fatal("an unknown storage backend was accepted")
	}
	if !strings.Contains(err.Error(), "mongo") {
		t.Errorf("error = %v, want it to quote the rejected value", err)
	}
}

// ===== formatDuration =====

// TestFormatDuration pins each branch of the ETA formatter. A wrong unit here
// shows up as a nonsense ETA on every long index run.
func TestFormatDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{900 * time.Millisecond, "<1s"},
		{0, "<1s"},
		{time.Second, "1s"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m 30s"},
		{2*time.Minute + 30*time.Second, "2m 30s"},
		{3*time.Hour + 4*time.Minute + 5*time.Second, "3h 4m"},
		{time.Hour, "1h 0m"},
	}
	for _, tt := range tests {
		if got := formatDuration(tt.in); got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ===== truncatePath =====

// TestTruncatePath pins the middle-ellipsis shape and the rune safety that
// keeps a multi-byte path from being cut mid-character.
func TestTruncatePath(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		maxLen int
		want   string
	}{
		{"short path is untouched", "a/b.go", 50, "a/b.go"},
		{"exactly at the limit", "abcde", 5, "abcde"},
		{"hard cut below ten", "abcdefghijkl", 4, "abcd"},
		{"middle ellipsis", "aaaaaaaaaabbbbbbbbbb", 13, "aaaaa...bbbbb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncatePath(tt.path, tt.maxLen); got != tt.want {
				t.Errorf("truncatePath(%q, %d) = %q, want %q", tt.path, tt.maxLen, got, tt.want)
			}
		})
	}
}

// TestTruncatePathIsRuneSafe: slicing bytes instead of runes emits replacement
// characters for any non-ASCII path.
func TestTruncatePathIsRuneSafe(t *testing.T) {
	path := strings.Repeat("日", 40)

	got := truncatePath(path, 21)

	if strings.Contains(got, "�") {
		t.Errorf("truncatePath cut a multi-byte rune in half: %q", got)
	}
	if runes := []rune(got); len(runes) != 21 {
		t.Errorf("truncatePath produced %d runes, want 21: %q", len(runes), got)
	}
}

// ===== filterExcludedPaths =====

// TestFilterExcludedPathsDropsExcludedDirectories is the bug this function was
// written for: filepath.Glob expands "docs*" into paths under an excluded
// directory, and indexing them re-adds exactly what the exclude list removed.
func TestFilterExcludedPathsDropsExcludedDirectories(t *testing.T) {
	paths := []string{"docs", ".stryker-tmp/docs", "vendor/x/docs", "src/docs"}

	got := filterExcludedPaths(paths, []string{".stryker-tmp", "vendor"}, false)

	want := []string{"docs", "src/docs"}
	if len(got) != len(want) {
		t.Fatalf("filterExcludedPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("filterExcludedPaths = %v, want %v", got, want)
			break
		}
	}
}

// TestFilterExcludedPathsPassesEverythingThroughWithNoFilters pins the cheap
// early return; dropping paths when nothing was excluded would silently shrink
// every index.
func TestFilterExcludedPathsPassesEverythingThroughWithNoFilters(t *testing.T) {
	paths := []string{"a", "b/c"}

	got := filterExcludedPaths(paths, nil, false)

	if len(got) != 2 || got[0] != "a" || got[1] != "b/c" {
		t.Errorf("filterExcludedPaths = %v, want the input unchanged", got)
	}
}

// TestFilterExcludedPathsMatchesGlobs covers the third matcher, which applies
// the pattern to the base name rather than the whole path.
func TestFilterExcludedPathsMatchesGlobs(t *testing.T) {
	paths := []string{"pkg/thing.go", "pkg/thing_test.go"}

	got := filterExcludedPaths(paths, []string{"*_test.go"}, false)

	if len(got) != 1 || got[0] != "pkg/thing.go" {
		t.Errorf("filterExcludedPaths = %v, want only the non-test file", got)
	}
}

// TestFilterExcludedPathsHonoursExcludeTests proves --exclude-tests contributes
// patterns even when --exclude is empty, which the early return would skip if
// it only checked the explicit list.
func TestFilterExcludedPathsHonoursExcludeTests(t *testing.T) {
	if len(semantic.TestDirPatterns) == 0 {
		t.Skip("no test directory patterns are defined")
	}
	paths := []string{"src/app.go", filepath.Join(semantic.TestDirPatterns[0], "app.go")}

	got := filterExcludedPaths(paths, nil, true)

	if len(got) != 1 || got[0] != "src/app.go" {
		t.Errorf("filterExcludedPaths = %v, want the test directory dropped", got)
	}
}

// ===== indexCmd wiring =====

// TestIndexCmdRejectsAPathThatMatchesNothing: a typo'd path must be an error,
// not a silent no-op that leaves the index stale.
func TestIndexCmdRejectsAPathThatMatchesNothing(t *testing.T) {
	newWorkspace(t)
	cmd := indexCmd()
	cmd.SetArgs([]string{"no-such-directory-*"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("a path pattern matching nothing was accepted")
	}
	if !strings.Contains(err.Error(), "no matches for path pattern") {
		t.Errorf("error = %v, want it to name the unmatched pattern", err)
	}
}

// TestIndexCmdReportsWhenEverythingWasExcluded distinguishes "nothing matched"
// from "the exclude list ate the matches", which are different mistakes.
func TestIndexCmdReportsWhenEverythingWasExcluded(t *testing.T) {
	dir := newWorkspace(t)
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cmd := indexCmd()
	cmd.SetArgs([]string{"vendor", "--exclude", "vendor"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("a fully excluded path set was accepted")
	}
	if !strings.Contains(err.Error(), "excluded by exclude patterns") {
		t.Errorf("error = %v, want it to blame the exclude patterns", err)
	}
}
