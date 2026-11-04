package commands

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// buildIndex indexes the working directory so an update has something to
// update. Calibration is skipped because it is a separate concern and costs
// probe queries on every call.
func buildIndex(t *testing.T) {
	t.Helper()
	captureStdout(t, func() {
		if err := runIndex(context.Background(), ".", indexOpts{skipCalibration: true}); err != nil {
			t.Fatalf("runIndex: %v", err)
		}
	})
}

// ===== runIndexUpdate =====

// TestIndexUpdateFullScanReindexesAChangedFile pins the fallback mode: with no
// git, every file is checked and only the modified one is re-embedded.
func TestIndexUpdateFullScanReindexesAChangedFile(t *testing.T) {
	dir := newWorkspace(t)
	fe := newFakeEmbedder(t)
	useEmbedder(t, fe)
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)
	buildIndex(t)

	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource+"\nfunc Added() {}\n")
	callsBefore := fe.callCount()

	out := captureStdout(t, func() {
		if err := runIndexUpdate(context.Background(), ".", updateOpts{noGit: true}); err != nil {
			t.Fatalf("runIndexUpdate: %v", err)
		}
	})

	if !strings.Contains(out, "(full scan)") {
		t.Errorf("--no-git did not take the full-scan path:\n%s", out)
	}
	if !strings.Contains(out, "Updated 1 files") {
		t.Errorf("the changed file was not re-indexed:\n%s", out)
	}
	if !strings.Contains(out, "(full mode)") {
		t.Errorf("the reported mode is wrong:\n%s", out)
	}
	if fe.callCount() == callsBefore {
		t.Error("the update re-indexed nothing; no embedding call was made")
	}
}

// TestIndexUpdateDoesNothingWhenNothingChanged keeps the commit hook cheap.
// Re-embedding an unchanged tree on every commit is the failure here.
func TestIndexUpdateDoesNothingWhenNothingChanged(t *testing.T) {
	dir := newWorkspace(t)
	fe := newFakeEmbedder(t)
	useEmbedder(t, fe)
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)
	buildIndex(t)
	callsBefore := fe.callCount()

	out := captureStdout(t, func() {
		if err := runIndexUpdate(context.Background(), ".", updateOpts{noGit: true}); err != nil {
			t.Fatalf("runIndexUpdate: %v", err)
		}
	})

	if !strings.Contains(out, "Updated 0 files") {
		t.Errorf("an unchanged tree reported work:\n%s", out)
	}
	if fe.callCount() != callsBefore {
		t.Errorf("an unchanged tree made %d extra embedding calls", fe.callCount()-callsBefore)
	}
}

// TestIndexUpdateRemovesChunksForADeletedFile: leaving them behind means the
// search keeps returning code that no longer exists.
func TestIndexUpdateRemovesChunksForADeletedFile(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "keep.go"), sampleGoSource)
	writeFile(t, filepath.Join(dir, "gone.go"), "package sample\n\nfunc Vanishing() {}\n")
	buildIndex(t)

	if err := os.Remove(filepath.Join(dir, "gone.go")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	out := captureStdout(t, func() {
		if err := runIndexUpdate(context.Background(), ".", updateOpts{noGit: true}); err != nil {
			t.Fatalf("runIndexUpdate: %v", err)
		}
	})

	if !strings.Contains(out, "removed 1 files") {
		t.Errorf("the deleted file was not removed from the index:\n%s", out)
	}
}

// TestIndexUpdateUsesGitWhenAvailable covers the default (git) branch, which
// diffs a ref instead of walking the whole tree.
func TestIndexUpdateUsesGitWhenAvailable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "first.go"), sampleGoSource)

	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"add", "-A"},
		{"commit", "-m", "first"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed in the sandbox: %v\n%s", args, err, out)
		}
	}
	buildIndex(t)

	writeFile(t, filepath.Join(dir, "second.go"), "package sample\n\nfunc SecondCommitSymbol() {}\n")
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "second"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	out := captureStdout(t, func() {
		if err := runIndexUpdate(context.Background(), ".", updateOpts{since: "HEAD~1"}); err != nil {
			t.Fatalf("runIndexUpdate: %v", err)
		}
	})

	if !strings.Contains(out, "git mode, since HEAD~1") {
		t.Errorf("the git path was not taken:\n%s", out)
	}
	if !strings.Contains(out, "Updated 1 files") {
		t.Errorf("the newly committed file was not indexed:\n%s", out)
	}
	if !strings.Contains(out, "(git mode)") {
		t.Errorf("the reported mode is wrong:\n%s", out)
	}
}

// TestIndexUpdateWithoutAnIndexTellsYouToBuildOne: updating nothing is not a
// silent no-op, because a hook that quietly does nothing never gets noticed.
func TestIndexUpdateWithoutAnIndexTellsYouToBuildOne(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))

	err := runIndexUpdate(context.Background(), ".", updateOpts{noGit: true})
	if err == nil {
		t.Fatal("updating with no index returned nil")
	}
	if !strings.Contains(err.Error(), "llm-semantic index") {
		t.Errorf("error = %v, want it to name the command that builds the index", err)
	}
}

// TestIndexUpdateSkipsWhileAnotherRunHoldsTheLock is the hook-safety rule: a
// commit during a rebuild must not block or corrupt, it must step aside.
func TestIndexUpdateSkipsWhileAnotherRunHoldsTheLock(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)
	buildIndex(t)

	indexPath := findIndexPath()
	if indexPath == "" {
		t.Fatal("the index the test just built cannot be found")
	}
	held := semantic.NewIndexLock(indexPath, "sqlite", resolveCollectionName())
	locked, err := held.TryLock()
	if err != nil || !locked {
		t.Fatalf("could not take the lock the test needs to hold: locked=%v err=%v", locked, err)
	}
	defer held.Unlock()

	out := captureStdout(t, func() {
		if err := runIndexUpdate(context.Background(), ".", updateOpts{noGit: true}); err != nil {
			t.Fatalf("a contended update must not error: %v", err)
		}
	})

	if !strings.Contains(out, "Another index operation is in progress, skipping update") {
		t.Errorf("the contended update did not step aside:\n%s", out)
	}
	if strings.Contains(out, "Updated") {
		t.Errorf("the contended update did work anyway:\n%s", out)
	}
}

// TestIndexUpdateStaysQuietUnderAXI: the "skipping" notice is exactly the kind
// of prose that makes a TOON stream unparseable.
func TestIndexUpdateStaysQuietUnderAXI(t *testing.T) {
	dir := newWorkspace(t)
	withAXI(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)
	captureStdout(t, func() {
		if err := runIndex(context.Background(), ".", indexOpts{skipCalibration: true}); err != nil {
			t.Fatalf("runIndex: %v", err)
		}
	})

	out := captureStdout(t, func() {
		if err := runIndexUpdate(context.Background(), ".", updateOpts{noGit: true}); err != nil {
			t.Fatalf("runIndexUpdate: %v", err)
		}
	})

	if strings.Contains(out, "Updating index for") || strings.Contains(out, "Updated 0 files") {
		t.Errorf("--axi output is interleaved with human text:\n%s", out)
	}
	if !strings.Contains(out, "mode: full") {
		t.Errorf("the TOON payload is missing the mode:\n%s", out)
	}
}

// newPoisonedEmbedder answers like newFakeEmbedder but rejects any request whose
// texts contain marker, so exactly one file fails to embed. 400 rather than 500
// because the embedder retries 5xx with backoff.
func newPoisonedEmbedder(t *testing.T, marker string) *fakeEmbedder {
	t.Helper()
	fe := &fakeEmbedder{}
	fe.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(marker)) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"poisoned input"}}`)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		fe.handle(w, r)
	}))
	t.Cleanup(fe.Server.Close)
	return fe
}

// TestIndexUpdateCountsAndNamesAFileItCouldNotProcess defends against
// index-update telling a caller "Updated N files" while a file whose embed
// failed vanished from the count and the output. On both the full-scan and the
// git path, the text and --json output must carry the skipped count and name
// the file, and the file must not be in the index.
func TestIndexUpdateCountsAndNamesAFileItCouldNotProcess(t *testing.T) {
	const poisonSource = "package sample\n\nfunc PoisonedSymbol() {}\n"

	for _, mode := range []struct {
		name  string
		opts  updateOpts
		setup func(t *testing.T, dir string)
	}{
		{"no-git", updateOpts{noGit: true}, func(t *testing.T, dir string) {
			buildIndex(t)
			writeFile(t, filepath.Join(dir, "added.go"), "package sample\n\nfunc AddedSymbol() {}\n")
			writeFile(t, filepath.Join(dir, "poison.go"), poisonSource)
		}},
		{"git", updateOpts{since: "HEAD~1"}, func(t *testing.T, dir string) {
			if _, err := exec.LookPath("git"); err != nil {
				t.Skip("git is not installed")
			}
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Skipf("git %v failed in the sandbox: %v\n%s", args, err, out)
				}
			}
			git("init")
			git("config", "user.email", "test@example.com")
			git("config", "user.name", "Test")
			git("add", "sample.go")
			git("commit", "-m", "first")
			buildIndex(t)
			writeFile(t, filepath.Join(dir, "added.go"), "package sample\n\nfunc AddedSymbol() {}\n")
			writeFile(t, filepath.Join(dir, "poison.go"), poisonSource)
			git("add", "added.go", "poison.go")
			git("commit", "-m", "second")
		}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			dir := newWorkspace(t)
			useEmbedder(t, newPoisonedEmbedder(t, "PoisonedSymbol"))
			writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)
			mode.setup(t, dir)

			text := captureStdout(t, func() {
				if err := runIndexUpdate(context.Background(), ".", mode.opts); err != nil {
					t.Fatalf("runIndexUpdate: %v", err)
				}
			})
			if !strings.Contains(text, "Updated 1 files") {
				t.Errorf("the file that embedded fine was not counted:\n%s", text)
			}
			if !strings.Contains(text, "Skipped 1 files with errors") {
				t.Errorf("the failed file is not counted in the text output:\n%s", text)
			}
			if !strings.Contains(text, "poison.go") || !strings.Contains(text, "poisoned input") {
				t.Errorf("the text output does not name the failed file and its error:\n%s", text)
			}

			// The failed file is still unindexed, so a second run fails it again.
			jsonOpts := mode.opts
			jsonOpts.jsonOutput = true
			out := captureStdout(t, func() {
				if err := runIndexUpdate(context.Background(), ".", jsonOpts); err != nil {
					t.Fatalf("runIndexUpdate --json: %v", err)
				}
			})
			var payload struct {
				FilesSkipped int      `json:"files_skipped"`
				Errors       []string `json:"errors"`
			}
			decodeJSON(t, out, &payload)
			if payload.FilesSkipped != 1 || len(payload.Errors) != 1 || !strings.Contains(payload.Errors[0], "poison.go") {
				t.Errorf("--json payload = %+v, want files_skipped 1 and an error naming poison.go", payload)
			}

			storage, err := semantic.NewSQLiteStorage(filepath.Join(".index", "semantic.db"), 0)
			if err != nil {
				t.Fatalf("NewSQLiteStorage: %v", err)
			}
			defer storage.Close()
			indexed, err := storage.ListIndexedFiles(context.Background())
			if err != nil {
				t.Fatalf("ListIndexedFiles: %v", err)
			}
			var names []string
			for path := range indexed {
				names = append(names, filepath.Base(path))
			}
			joined := strings.Join(names, ",")
			if !strings.Contains(joined, "added.go") || strings.Contains(joined, "poison.go") {
				t.Errorf("indexed files = %v, want added.go and not poison.go", names)
			}
		})
	}
}

// ===== reportUpdateResult =====

// TestReportUpdateResultText pins the two-line human summary; both counts are
// what tells a caller whether the hook actually did anything.
func TestReportUpdateResultText(t *testing.T) {
	result := &semantic.UpdateResult{
		FilesUpdated: 3, FilesRemoved: 1, ChunksCreated: 12, ChunksRemoved: 4, Mode: "git",
	}

	out := captureStdout(t, func() {
		if err := reportUpdateResult(result, false); err != nil {
			t.Fatalf("reportUpdateResult: %v", err)
		}
	})

	if !strings.Contains(out, "Updated 3 files, removed 1 files (git mode)") {
		t.Errorf("file summary is wrong:\n%s", out)
	}
	if !strings.Contains(out, "Created 12 chunks, removed 4 chunks") {
		t.Errorf("chunk summary is wrong:\n%s", out)
	}
}

// TestReportUpdateResultJSON pins the machine shape, which does carry json
// tags — unlike IndexResult, so the keys are snake_case here.
func TestReportUpdateResultJSON(t *testing.T) {
	result := &semantic.UpdateResult{
		FilesUpdated: 3, FilesRemoved: 1, ChunksCreated: 12, ChunksRemoved: 4, Mode: "full",
	}

	out := captureStdout(t, func() {
		if err := reportUpdateResult(result, true); err != nil {
			t.Fatalf("reportUpdateResult: %v", err)
		}
	})

	var payload struct {
		FilesUpdated  int    `json:"files_updated"`
		ChunksRemoved int    `json:"chunks_removed"`
		Mode          string `json:"mode"`
	}
	decodeJSON(t, out, &payload)
	if payload.FilesUpdated != 3 || payload.ChunksRemoved != 4 || payload.Mode != "full" {
		t.Errorf("payload = %+v, want the result verbatim", payload)
	}
}

// TestReportUpdateResultAXIIgnoresTheJSONFlag: --axi has to win on its own,
// because no caller passes both.
func TestReportUpdateResultAXIIgnoresTheJSONFlag(t *testing.T) {
	withAXI(t)
	result := &semantic.UpdateResult{FilesUpdated: 2, Mode: "git"}

	out := captureStdout(t, func() {
		if err := reportUpdateResult(result, false); err != nil {
			t.Fatalf("reportUpdateResult: %v", err)
		}
	})

	if strings.Contains(out, "Updated 2 files") {
		t.Errorf("--axi emitted the human summary:\n%s", out)
	}
	if !strings.Contains(out, "files_updated: 2") {
		t.Errorf("--axi did not emit TOON:\n%s", out)
	}
}

// ===== indexUpdateCmd wiring =====

// TestIndexUpdateCmdRejectsAPathThatMatchesNothing mirrors the index command,
// so a typo fails the same way in both.
func TestIndexUpdateCmdRejectsAPathThatMatchesNothing(t *testing.T) {
	newWorkspace(t)
	cmd := indexUpdateCmd()
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
