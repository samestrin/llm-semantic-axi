package commands

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// Epic 05.1 T4. --axi search rows carry a small fixed field set and cut long
// content with a size hint; --full restores everything; --json is untouched.

func longResult(n int) semantic.SearchResult {
	return semantic.SearchResult{
		Score: 0.5,
		Chunk: semantic.Chunk{
			ID: "x", FilePath: "a.go", Name: "Big",
			StartLine: 3, EndLine: 90, Language: "go",
			Content: strings.Repeat("x", n),
		},
	}
}

// TestAXIRowsCarryFourFieldsAndTruncateLongContent: a full chunk per hit
// (id, hash, language, whole body) is what makes --axi lists expensive.
func TestAXIRowsCarryFourFieldsAndTruncateLongContent(t *testing.T) {
	rows := axiSearchRows([]semantic.SearchResult{longResult(5000)})
	if len(rows) != 1 || len(rows[0]) != 4 {
		t.Fatalf("rows = %v, want one row with 4 fields", rows)
	}
	for _, k := range []string{"file", "name", "score", "content"} {
		if _, ok := rows[0][k]; !ok {
			t.Errorf("row lacks %q: %v", k, rows[0])
		}
	}
	if rows[0]["file"] != "a.go:3" {
		t.Errorf("file = %v, want a.go:3", rows[0]["file"])
	}
	c := rows[0]["content"].(string)
	if len(c) >= 1000 || !strings.Contains(c, "4800 more chars") || !strings.Contains(c, "--full") {
		t.Errorf("content not truncated with a size hint: %q", c)
	}
}

// TestAXIRowsKeepShortContentWhole: a hint on content that was not cut lies.
func TestAXIRowsKeepShortContentWhole(t *testing.T) {
	rows := axiSearchRows([]semantic.SearchResult{longResult(50)})
	if c := rows[0]["content"].(string); c != strings.Repeat("x", 50) {
		t.Errorf("short content altered: %q", c)
	}
}

// longBody is a function body well over axiContentLimit.
var longBody = "func LongFunc() string {\n" + strings.Repeat("\t// padding padding padding\n", 30) + "\treturn \"TAILMARKER\"\n}\n"

// indexedWithLongFunc is indexedWorkspace plus a function longer than the cut.
func indexedWithLongFunc(t *testing.T) {
	t.Helper()
	dir := indexedWorkspace(t)
	writeFile(t, filepath.Join(dir, "long.go"), "package sample\n\n"+longBody)
	captureStdout(t, func() {
		if err := runIndex(context.Background(), ".", indexOpts{skipCalibration: true}); err != nil {
			t.Fatalf("runIndex: %v", err)
		}
	})
}

// TestAXIFullReturnsWholeContent: --full is the escape hatch from truncation;
// the tail of a long body must survive it and be lost without it.
func TestAXIFullReturnsWholeContent(t *testing.T) {
	indexedWithLongFunc(t)
	withAXI(t)
	run := func(full bool) string {
		return captureStdout(t, func() {
			if err := runSearch(context.Background(), "LongFunc", searchOpts{topK: 20, full: full}); err != nil {
				t.Fatalf("runSearch: %v", err)
			}
		})
	}
	def, full := run(false), run(true)
	if !strings.Contains(def, "more chars") || strings.Contains(def, "TAILMARKER") {
		t.Errorf("default output not truncated:\n%s", def)
	}
	if !strings.Contains(full, "TAILMARKER") || strings.Contains(full, "more chars") {
		t.Errorf("--full did not return the whole content:\n%s", full)
	}
	for _, want := range []string{"file_path", "start_line", "language"} {
		if !strings.Contains(full, want) {
			t.Errorf("--full output lacks %q", want)
		}
	}
}

// TestMultisearchAXIShapedAndFull: multisearch is part of the --axi search
// family; its default must be the four-field row and --full the raw result.
func TestMultisearchAXIShapedAndFull(t *testing.T) {
	indexedWithLongFunc(t)
	withAXI(t)
	run := func(full bool) string {
		return captureStdout(t, func() {
			if err := runMultisearch(context.Background(), []string{"LongFunc", "long padding"}, multisearchOpts{topK: 20, full: full}); err != nil {
				t.Fatalf("runMultisearch: %v", err)
			}
		})
	}
	def, full := run(false), run(true)
	if !strings.Contains(def, "more chars") || strings.Contains(def, "TAILMARKER") || strings.Contains(def, "start_line") {
		t.Errorf("default multisearch not shaped:\n%s", def)
	}
	if !strings.Contains(def, "total_results") || !strings.Contains(def, "help[") {
		t.Errorf("default multisearch lost its totals or help:\n%s", def)
	}
	if !strings.Contains(full, "TAILMARKER") || strings.Contains(full, "more chars") || !strings.Contains(full, "start_line") {
		t.Errorf("--full multisearch is not the whole result:\n%s", full)
	}
}

// TestMultisearchAXIGroupedFormatsAreShaped: by_query and by_collection hold
// the same fat chunks and must be cut too.
func TestMultisearchAXIGroupedFormatsAreShaped(t *testing.T) {
	indexedWithLongFunc(t)
	withAXI(t)
	for _, f := range []semantic.OutputFormat{semantic.OutputByQuery, semantic.OutputByCollection} {
		out := captureStdout(t, func() {
			if err := runMultisearch(context.Background(), []string{"LongFunc"}, multisearchOpts{topK: 20, outputFormat: f}); err != nil {
				t.Fatalf("runMultisearch: %v", err)
			}
		})
		if strings.Contains(out, "TAILMARKER") || !strings.Contains(out, "more chars") {
			t.Errorf("%s not shaped:\n%s", f, out)
		}
	}
}

// TestMemorySearchAXIShapedAndFull: a memory's whole answer per hit is the
// same cost problem as a whole chunk.
func TestMemorySearchAXIShapedAndFull(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-long", "How should authentication tokens be validated?", strings.Repeat("verify ", 100)+"ENDMARKER"))
	withAXI(t)
	run := func(opts memorySearchOpts) string {
		return captureStdout(t, func() {
			if err := runMemorySearch(context.Background(), opts); err != nil {
				t.Fatalf("runMemorySearch: %v", err)
			}
		})
	}
	def := run(memorySearchOpts{query: "authentication tokens", topK: 5})
	if !strings.Contains(def, "mem-long") || !strings.Contains(def, "more chars") || strings.Contains(def, "ENDMARKER") || strings.Contains(def, "occurrences") {
		t.Errorf("default memory row not shaped:\n%s", def)
	}
	full := run(memorySearchOpts{query: "authentication tokens", topK: 5, full: true})
	if !strings.Contains(full, "ENDMARKER") || strings.Contains(full, "more chars") || !strings.Contains(full, "occurrences") {
		t.Errorf("--full memory is not the whole entry:\n%s", full)
	}
}

// TestAXIDefaultOmitsChunkInternals: end to end, the default view drops
// fields only --full carries.
func TestAXIDefaultOmitsChunkInternals(t *testing.T) {
	indexedWorkspace(t)
	withAXI(t)
	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate token", searchOpts{topK: 1}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})
	for _, bad := range []string{"file_path", "start_line", "language", "content_hash"} {
		if strings.Contains(out, bad) {
			t.Errorf("default --axi carries %q:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "sample.go:") {
		t.Errorf("default --axi row carries no file:line:\n%s", out)
	}
}

// TestJSONOutputUnchangedByAXIShaping: --json's schema is fixed (board,
// 2026-10-31); it must still be the raw SearchResult list, with or without --full.
func TestJSONOutputUnchangedByAXIShaping(t *testing.T) {
	rs := []semantic.SearchResult{longResult(5000)}
	want, _ := json.Marshal(searchRows(rs, false))
	if !strings.Contains(string(want), strings.Repeat("x", 5000)) || !strings.Contains(string(want), `"file_path"`) {
		t.Fatalf("--json rows are shaped: %.200s", want)
	}
	indexedWorkspace(t)
	a := captureStdout(t, func() {
		_ = runSearch(context.Background(), "authenticate token", searchOpts{topK: 3, jsonOutput: true})
	})
	b := captureStdout(t, func() {
		_ = runSearch(context.Background(), "authenticate token", searchOpts{topK: 3, jsonOutput: true, full: true})
	})
	if a != b || !strings.Contains(a, `"file_path"`) {
		t.Errorf("--json differs with --full:\n%s\n%s", a, b)
	}
}
