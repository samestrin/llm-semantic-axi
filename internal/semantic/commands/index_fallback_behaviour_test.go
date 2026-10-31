package commands

import (
	"context"
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
		FilesFallback *int `json:"FilesFallback"`
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
		FilesFallback int      `json:"FilesFallback"`
		FallbackFiles []string `json:"FallbackFiles"`
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
