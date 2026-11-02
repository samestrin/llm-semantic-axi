package commands

import (
	"context"
	"encoding/json"
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

// TestAXIFullReturnsWholeContent: --full is the escape hatch from truncation.
func TestAXIFullReturnsWholeContent(t *testing.T) {
	indexedWorkspace(t)
	withAXI(t)
	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate token", searchOpts{topK: 1, full: true}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})
	for _, want := range []string{"file_path", "content", "start_line", "language"} {
		if !strings.Contains(out, want) {
			t.Errorf("--full output lacks %q:\n%s", want, out)
		}
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
