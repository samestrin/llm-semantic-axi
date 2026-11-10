package commands

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

const cleanPySource = "def greet(name):\n    return 'hi ' + name\n"

// brokenPySource does not parse: the def has no colon and the call is unclosed.
const brokenPySource = "def broken(x\n    return print(x\n\nclass Fine:\n    pass\n"

// storedChunks reopens the working directory's index and returns its chunks
// keyed by file name.
func storedChunks(t *testing.T) map[string][]semantic.Chunk {
	t.Helper()
	storage, err := semantic.NewSQLiteStorage(filepath.Join(".index", "semantic.db"), 0)
	if err != nil {
		t.Fatalf("reopen index: %v", err)
	}
	defer storage.Close()
	chunks, err := storage.List(context.Background(), semantic.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byFile := map[string][]semantic.Chunk{}
	for _, c := range chunks {
		byFile[filepath.Base(c.FilePath)] = append(byFile[filepath.Base(c.FilePath)], c)
	}
	return byFile
}

// TestRunIndexStoresTextWindowsForAnUnparseableFileAndSaysSo is task T2's
// "done when": before the fallback, a Python file with a syntax error indexed
// as whatever fragments the walk found (here: none of its broken function),
// and the summary called it indexed like any other file.
func TestRunIndexStoresTextWindowsForAnUnparseableFileAndSaysSo(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "good.py"), cleanPySource)
	writeFile(t, filepath.Join(dir, "broken.py"), brokenPySource)

	out := runIndexIn(t, ".", indexOpts{skipCalibration: true})

	if !strings.Contains(out, "Chunked 1 files as text windows (parse failed)") {
		t.Errorf("summary does not state the fallback count:\n%s", out)
	}
	if !strings.Contains(out, "broken.py") {
		t.Errorf("summary does not name the file that fell back:\n%s", out)
	}

	byFile := storedChunks(t)
	broken := byFile["broken.py"]
	if len(broken) == 0 {
		t.Fatal("no chunks stored for broken.py: the file dropped silently")
	}
	for _, c := range broken {
		if c.Type != semantic.ChunkFile {
			t.Errorf("broken.py chunk %q has type %q, want text windows (%q)", c.Name, c.Type, semantic.ChunkFile)
		}
	}
	if !strings.Contains(broken[0].Content, "def broken(x") {
		t.Errorf("broken.py text window does not hold the file's text: %q", broken[0].Content)
	}

	good := byFile["good.py"]
	if len(good) != 1 || good[0].Name != "greet" || good[0].Type != semantic.ChunkFunction {
		t.Errorf("good.py chunks = %+v, want the tree-sitter function `greet`", good)
	}
}

// TestRunIndexReportsNoFallbackForACleanFixture is the other half of "done
// when": a clean tree must not carry a fallback line, and the machine payload
// must say zero rather than leave the key out.
func TestRunIndexReportsNoFallbackForACleanFixture(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "good.py"), cleanPySource)

	out := runIndexIn(t, ".", indexOpts{skipCalibration: true})
	if strings.Contains(out, "text windows") {
		t.Errorf("a clean fixture reported a fallback:\n%s", out)
	}

	out = runIndexIn(t, ".", indexOpts{jsonOutput: true, skipCalibration: true, force: true})
	var payload struct {
		FilesFallback *int `json:"files_fallback"`
	}
	decodeJSON(t, out, &payload)
	if payload.FilesFallback == nil || *payload.FilesFallback != 0 {
		t.Errorf("FilesFallback = %v, want an explicit 0\n%s", payload.FilesFallback, out)
	}
}

// TestRunIndexJSONCarriesTheFallbackCount: an agent reading --json must see
// the count, or a broken file looks parsed to it.
func TestRunIndexJSONCarriesTheFallbackCount(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "broken.py"), brokenPySource)

	out := runIndexIn(t, ".", indexOpts{jsonOutput: true, skipCalibration: true})

	var payload struct {
		FilesFallback int      `json:"files_fallback"`
		FallbackFiles []string `json:"fallback_files"`
	}
	decodeJSON(t, out, &payload)
	if payload.FilesFallback != 1 || len(payload.FallbackFiles) != 1 || filepath.Base(payload.FallbackFiles[0]) != "broken.py" {
		t.Errorf("payload = %+v, want broken.py counted", payload)
	}
}

// TestIndexUpdateStatesTheFallbackCount: an edit that breaks a file's syntax
// is the common way a file starts failing to parse, and index-update is what
// the commit hook runs. It has to say so there too.
func TestIndexUpdateStatesTheFallbackCount(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "good.py"), cleanPySource)
	buildIndex(t)

	writeFile(t, filepath.Join(dir, "good.py"), brokenPySource)
	out := captureStdout(t, func() {
		if err := runIndexUpdate(context.Background(), ".", updateOpts{noGit: true}); err != nil {
			t.Fatalf("runIndexUpdate: %v", err)
		}
	})

	if !strings.Contains(out, "Chunked 1 files as text windows (parse failed)") {
		t.Errorf("index-update does not state the fallback count:\n%s", out)
	}
	if !strings.Contains(out, "good.py") {
		t.Errorf("index-update does not name the file that fell back:\n%s", out)
	}
	for _, c := range storedChunks(t)["good.py"] {
		if c.Type != semantic.ChunkFile {
			t.Errorf("good.py chunk %q has type %q after the update, want text windows", c.Name, c.Type)
		}
	}
}

// TestReportUpdateResultOmitsTheFallbackLineWhenNoneFellBack keeps the clean
// commit-hook output unchanged.
func TestReportUpdateResultOmitsTheFallbackLineWhenNoneFellBack(t *testing.T) {
	out := captureStdout(t, func() {
		if err := reportUpdateResult(&semantic.UpdateResult{FilesUpdated: 1, Mode: "full"}, false); err != nil {
			t.Fatalf("reportUpdateResult: %v", err)
		}
	})
	if strings.Contains(out, "text windows") {
		t.Errorf("a clean update reported a fallback:\n%s", out)
	}
}

// TestRunIndexKeepsNamedChunksForAPartlyParsedFile pins the board's choice on
// EST-3580 (hybrid) in the database: the function the grammar did parse keeps
// its named row, the code it swallowed is stored as a text window, and the
// file is still counted. Whole-file fallback dropped the named row; keeping
// only the named rows dropped `after`.
func TestRunIndexKeepsNamedChunksForAPartlyParsedFile(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "partial.py"),
		"import os\n\ndef ok(x):\n    return x\n\nwhile True print(\n\ndef after(y):\n    return y\n")

	out := runIndexIn(t, ".", indexOpts{skipCalibration: true})
	if !strings.Contains(out, "Chunked 1 files as text windows (parse failed)") {
		t.Errorf("summary does not count the partly parsed file:\n%s", out)
	}

	var named, windowText []string
	for _, c := range storedChunks(t)["partial.py"] {
		if c.Type == semantic.ChunkFile {
			windowText = append(windowText, c.Content)
		} else {
			named = append(named, c.Name)
		}
	}
	if len(named) != 1 || named[0] != "ok" {
		t.Errorf("named rows = %v, want [ok]", named)
	}
	if !strings.Contains(strings.Join(windowText, "\n"), "def after(y):") {
		t.Errorf("no text window row holds `def after`; windows = %q", windowText)
	}
}

// TestResumedIndexAndIndexStatusStillReportTheFallback defends the fallback
// surviving the run that found it: a second `index` skipped the unchanged file
// and said nothing about it, and index-status had no way to show it, so the
// only record that a file was searchable as text windows alone was gone.
func TestResumedIndexAndIndexStatusStillReportTheFallback(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "good.py"), cleanPySource)
	writeFile(t, filepath.Join(dir, "broken.py"), brokenPySource)
	runIndexIn(t, ".", indexOpts{skipCalibration: true})

	resumed := runIndexIn(t, ".", indexOpts{skipCalibration: true})
	if !strings.Contains(resumed, "Skipped 2 unchanged files") {
		t.Fatalf("the second index did not resume:\n%s", resumed)
	}
	if !strings.Contains(resumed, "Chunked 1 files as text windows (parse failed)") || !strings.Contains(resumed, "broken.py") {
		t.Errorf("the resumed index does not report the file that fell back:\n%s", resumed)
	}

	plain := captureStdout(t, func() {
		if err := runIndexStatus(context.Background(), false); err != nil {
			t.Fatalf("index-status: %v", err)
		}
	})
	if !strings.Contains(plain, "Text windows:  1 files") || !strings.Contains(plain, "broken.py (parse failed)") {
		t.Errorf("index-status does not show the file that fell back:\n%s", plain)
	}

	raw := captureStdout(t, func() {
		if err := runIndexStatus(context.Background(), true); err != nil {
			t.Fatalf("index-status --json: %v", err)
		}
	})
	var payload struct {
		FilesFallback *int     `json:"files_fallback"`
		FallbackFiles []string `json:"fallback_files"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("index-status --json: %v\n%s", err, raw)
	}
	if payload.FilesFallback == nil || *payload.FilesFallback != 1 || len(payload.FallbackFiles) != 1 || filepath.Base(payload.FallbackFiles[0]) != "broken.py" {
		t.Errorf("index-status --json fallback = %v %v, want 1 naming broken.py:\n%s", payload.FilesFallback, payload.FallbackFiles, raw)
	}
}

// TestIndexStatusLabelsAChunkerFailureAsOne defends index-status keeping the
// two fallback reasons apart: a file whose chunker errored or panicked must not
// read as "(parse failed)", which blames its syntax, the mislabel `index` and
// `index-update` were fixed for. No shipped chunker panics on demand, so the
// record is written straight to the index after a real run.
func TestIndexStatusLabelsAChunkerFailureAsOne(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "good.py"), cleanPySource)
	writeFile(t, filepath.Join(dir, "broken.py"), brokenPySource)
	runIndexIn(t, ".", indexOpts{skipCalibration: true})

	storage, err := semantic.NewSQLiteStorage(filepath.Join(".index", "semantic.db"), 0)
	if err != nil {
		t.Fatalf("reopen index: %v", err)
	}
	good := filepath.Join(dir, "good.py")
	hash, err := storage.GetFileHash(context.Background(), good)
	if err != nil || hash == "" {
		storage.Close()
		t.Fatalf("good.py has no stored hash (%q, %v); the test needs it indexed", hash, err)
	}
	if err := storage.SetFileHashWithFallback(context.Background(), good, hash, semantic.FallbackChunkerFailed); err != nil {
		storage.Close()
		t.Fatalf("record the chunker failure: %v", err)
	}
	storage.Close()

	plain := captureStdout(t, func() {
		if err := runIndexStatus(context.Background(), false); err != nil {
			t.Fatalf("index-status: %v", err)
		}
	})
	for _, want := range []string{"Text windows:  2 files", "good.py (chunker failed)", "broken.py (parse failed)"} {
		if !strings.Contains(plain, want) {
			t.Errorf("index-status lacks %q:\n%s", want, plain)
		}
	}

	raw := captureStdout(t, func() {
		if err := runIndexStatus(context.Background(), true); err != nil {
			t.Fatalf("index-status --json: %v", err)
		}
	})
	var payload struct {
		FallbackFiles              []string `json:"fallback_files"`
		FallbackChunkerFailedFiles []string `json:"fallback_chunker_failed_files"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("index-status --json: %v\n%s", err, raw)
	}
	if len(payload.FallbackFiles) != 2 || len(payload.FallbackChunkerFailedFiles) != 1 || filepath.Base(payload.FallbackChunkerFailedFiles[0]) != "good.py" {
		t.Errorf("index-status --json fallback_files = %v, fallback_chunker_failed_files = %v; want both files, and good.py alone as chunker failed:\n%s",
			payload.FallbackFiles, payload.FallbackChunkerFailedFiles, raw)
	}
}

// TestStatusFallbacksWithoutARecordIsNotZero defends index-status over a
// backend that keeps no fallback record: it must report the record as absent,
// so the keys are omitted, rather than a measured zero.
func TestStatusFallbacksWithoutARecordIsNotZero(t *testing.T) {
	fellBack, files, ok, err := statusFallbacks(context.Background(), lastIndexOnly{})
	if err != nil || ok || fellBack != nil || files != nil {
		t.Errorf("statusFallbacks on a backend without FallbackStore = %v, %v, ok %v, %v; want nothing and ok false", fellBack, files, ok, err)
	}

	raw, err := json.Marshal(indexStatusJSON{Indexed: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fallback") {
		t.Errorf("a status with no fallback record marshals %s, want no fallback keys", raw)
	}
}
