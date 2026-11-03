package commands

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// Epic 05.1 F1. A search over an index that exists but holds no chunks used to
// print "No results found for ..." (0 results), the same words as a search
// that ran and matched nothing. Empty there means "nothing was searched", and
// the answer has to say so and name `llm-semantic index` as the way out.

// emptyIndexWorkspace creates .index/semantic.db with its schema and no chunks.
func emptyIndexWorkspace(t *testing.T) {
	t.Helper()
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t)
}

// ===== search =====

func TestSearchPlainOverAnEmptyIndexSaysTheIndexIsEmpty(t *testing.T) {
	emptyIndexWorkspace(t)

	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate", searchOpts{topK: 5}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})

	if strings.Contains(out, "No results found") {
		t.Errorf("an empty index reads as a search that matched nothing:\n%s", out)
	}
	for _, want := range []string{"index is empty", "llm-semantic index <path>"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain output over an empty index lacks %q:\n%s", want, out)
		}
	}
}

func TestSearchAXIOverAnEmptyIndexCarriesTheStatus(t *testing.T) {
	emptyIndexWorkspace(t)
	withAXI(t)

	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate", searchOpts{topK: 5}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})

	for _, want := range []string{"status: index_empty\n", "total: 0\n", "index is empty", "llm-semantic index <path>"} {
		if !strings.Contains(out, want) {
			t.Errorf("--axi over an empty index lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "No results found") || strings.Contains(out, "--threshold 0") {
		t.Errorf("--axi over an empty index reads as a no-match, or suggests widening it:\n%s", out)
	}
}

// TestSearchJSONOverAnEmptyIndexKeepsItsSchema: --json keeps its schema (board
// decision, 2026-10-31); the diagnosis is --axi and plain only.
func TestSearchJSONOverAnEmptyIndexKeepsItsSchema(t *testing.T) {
	emptyIndexWorkspace(t)

	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate", searchOpts{topK: 5, jsonOutput: true}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})
	if out != "null\n" {
		t.Errorf("--json over an empty index changed: %q, want %q", out, "null\n")
	}
}

// TestSearchAXINoMatchOverAPopulatedIndexIsNotEmpty: the check must not turn
// a genuine no-match into "index empty".
func TestSearchAXINoMatchOverAPopulatedIndexIsNotEmpty(t *testing.T) {
	indexedWorkspace(t)
	withAXI(t)

	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate", searchOpts{topK: 5, threshold: 0.99}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})
	if !strings.Contains(out, "status: no_match\n") || !strings.Contains(out, "No results found") {
		t.Errorf("--axi no-match over a populated index lacks the no_match status or message:\n%s", out)
	}
	if strings.Contains(out, "index_empty") || strings.Contains(out, "index is empty") {
		t.Errorf("a no-match over a populated index claims the index is empty:\n%s", out)
	}
}

// ===== multisearch =====

func TestMultisearchOverAnEmptyIndexSaysTheIndexIsEmpty(t *testing.T) {
	emptyIndexWorkspace(t)
	queries := []string{"authenticate", "invoice"}

	plain := captureStdout(t, func() {
		if err := runMultisearch(context.Background(), queries, multisearchOpts{topK: 5}); err != nil {
			t.Fatalf("runMultisearch: %v", err)
		}
	})
	if strings.Contains(plain, "No results found") || !strings.Contains(plain, "index is empty") || !strings.Contains(plain, "llm-semantic index <path>") {
		t.Errorf("plain multisearch over an empty index:\n%s", plain)
	}

	withAXI(t)
	axi := captureStdout(t, func() {
		if err := runMultisearch(context.Background(), queries, multisearchOpts{topK: 5}); err != nil {
			t.Fatalf("runMultisearch: %v", err)
		}
	})
	for _, want := range []string{"status: index_empty\n", "total_results: 0\n", "index is empty", "llm-semantic index <path>"} {
		if !strings.Contains(axi, want) {
			t.Errorf("--axi multisearch over an empty index lacks %q:\n%s", want, axi)
		}
	}
	if strings.Contains(axi, "No results found") {
		t.Errorf("--axi multisearch over an empty index reads as a no-match:\n%s", axi)
	}
}

func TestMultisearchJSONOverAnEmptyIndexKeepsItsSchema(t *testing.T) {
	emptyIndexWorkspace(t)

	out := captureStdout(t, func() {
		if err := runMultisearch(context.Background(), []string{"authenticate"}, multisearchOpts{topK: 5, jsonOutput: true}); err != nil {
			t.Fatalf("runMultisearch: %v", err)
		}
	})
	if strings.Contains(out, `"status"`) || strings.Contains(out, `"message"`) {
		t.Errorf("--json multisearch gained a key:\n%s", out)
	}
}

func TestMultisearchAXINoMatchOverAPopulatedIndexIsNotEmpty(t *testing.T) {
	indexedWorkspace(t)
	withAXI(t)

	out := captureStdout(t, func() {
		if err := runMultisearch(context.Background(), []string{"authenticate"}, multisearchOpts{topK: 5, threshold: 0.99}); err != nil {
			t.Fatalf("runMultisearch: %v", err)
		}
	})
	if !strings.Contains(out, "status: no_match\n") || strings.Contains(out, "index_empty") {
		t.Errorf("--axi multisearch no-match over a populated index:\n%s", out)
	}
}

// TestIndexIsEmptySurfacesAStatsFailure: if the count fails, guessing either
// answer would be the false report this exists to prevent.
func TestIndexIsEmptySurfacesAStatsFailure(t *testing.T) {
	emptyIndexWorkspace(t)
	storage, err := semantic.NewSQLiteStorage(filepath.Join(".index", "semantic.db"), 0)
	if err != nil {
		t.Fatalf("NewSQLiteStorage: %v", err)
	}
	storage.Close()

	if _, err := indexIsEmpty(context.Background(), storage); err == nil {
		t.Error("a failed count over a closed index returned no error")
	}
}
