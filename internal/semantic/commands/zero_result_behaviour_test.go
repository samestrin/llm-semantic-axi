package commands

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// Epic 05.1 T2. A search that found nothing has to say so in words, and a
// search that found something has to say how much, in --axi and plain. --json
// keeps its old shape (a bare array for search and memory-search), so those
// tests pin it byte for byte against the shape the old code produced.

// ===== search =====

// TestSearchPlainStatesTheTotal: a list with no count makes the reader count
// it, and a truncated pipe cannot be told from a short answer.
func TestSearchPlainStatesTheTotal(t *testing.T) {
	indexedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate token", searchOpts{topK: 1}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})

	if !strings.HasPrefix(out, "1 result for \"authenticate token\"\n") {
		t.Errorf("plain output does not open with the total:\n%s", out)
	}
}

// TestSearchPlainZeroResultsNamesTheQueryAndTheTotal: the old line, "No results
// found.", did not say what was searched for, and printed no count.
func TestSearchPlainZeroResultsNamesTheQueryAndTheTotal(t *testing.T) {
	indexedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate", searchOpts{topK: 5, threshold: 0.99}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})

	if out != "No results found for \"authenticate\" (0 results).\n" {
		t.Errorf("zero-result plain output = %q", out)
	}
}

// TestSearchAXIStatesTheTotal: --axi used to be the bare result array in TOON,
// with no count beside it.
func TestSearchAXIStatesTheTotal(t *testing.T) {
	indexedWorkspace(t)
	withAXI(t)

	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate token", searchOpts{topK: 1}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})

	if !strings.Contains(out, "total: 1\n") {
		t.Errorf("--axi carries no total:\n%s", out)
	}
	if strings.Contains(out, "message:") {
		t.Errorf("--axi with results carries a zero-result message:\n%s", out)
	}
}

// TestSearchAXIZeroResultsSaysSo: the old --axi zero-result output was an
// empty TOON array, which an agent reads as nothing at all.
func TestSearchAXIZeroResultsSaysSo(t *testing.T) {
	indexedWorkspace(t)
	withAXI(t)

	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate", searchOpts{topK: 5, threshold: 0.99}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})

	for _, want := range []string{"total: 0\n", `message: "No results found for \"authenticate\"."`} {
		if !strings.Contains(out, want) {
			t.Errorf("--axi zero-result output lacks %q:\n%s", want, out)
		}
	}
}

// TestSearchJSONKeepsItsBareArray: the board kept --json's schema (2026-10-31).
// A wrapper object would break every consumer that indexes the array.
func TestSearchJSONKeepsItsBareArray(t *testing.T) {
	indexedWorkspace(t)

	for _, opts := range []searchOpts{{topK: 1, jsonOutput: true}, {topK: 1, minOutput: true}} {
		out := captureStdout(t, func() {
			if err := runSearch(context.Background(), "authenticate token", opts); err != nil {
				t.Fatalf("runSearch: %v", err)
			}
		})
		if !strings.HasPrefix(out, "[") || strings.Contains(out, `"total"`) {
			t.Errorf("--json/--min (%+v) is no longer a bare array:\n%s", opts, out)
		}
	}

	zero := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate", searchOpts{topK: 5, threshold: 0.99, jsonOutput: true}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})
	if zero != "null\n" {
		t.Errorf("zero-result --json changed: %q, want the old %q", zero, "null\n")
	}
}

// ===== memory-search =====

func memoryWorkspace(t *testing.T) {
	t.Helper()
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t,
		newMemory("mem-auth", "How should authentication tokens be validated?", "Verify the signature"),
	)
}

func TestMemorySearchPlainStatesTheTotal(t *testing.T) {
	memoryWorkspace(t)

	out := captureStdout(t, func() {
		if err := runMemorySearch(context.Background(), memorySearchOpts{query: "authentication tokens", topK: 5}); err != nil {
			t.Fatalf("runMemorySearch: %v", err)
		}
	})

	if !strings.HasPrefix(out, "1 result for \"authentication tokens\"\n") {
		t.Errorf("plain output does not open with the total:\n%s", out)
	}
}

func TestMemorySearchZeroResultsInPlainAndAXI(t *testing.T) {
	memoryWorkspace(t)
	opts := memorySearchOpts{query: "authentication", topK: 5, threshold: 0.99}

	plain := captureStdout(t, func() {
		if err := runMemorySearch(context.Background(), opts); err != nil {
			t.Fatalf("runMemorySearch: %v", err)
		}
	})
	if plain != "No results found for \"authentication\" (0 results).\n" {
		t.Errorf("zero-result plain output = %q", plain)
	}

	withAXI(t)
	axi := captureStdout(t, func() {
		if err := runMemorySearch(context.Background(), opts); err != nil {
			t.Fatalf("runMemorySearch: %v", err)
		}
	})
	for _, want := range []string{"total: 0\n", `message: "No results found for \"authentication\"."`} {
		if !strings.Contains(axi, want) {
			t.Errorf("--axi zero-result output lacks %q:\n%s", want, axi)
		}
	}
}

func TestMemorySearchAXIStatesTheTotal(t *testing.T) {
	memoryWorkspace(t)
	withAXI(t)

	out := captureStdout(t, func() {
		if err := runMemorySearch(context.Background(), memorySearchOpts{query: "authentication tokens", topK: 5}); err != nil {
			t.Fatalf("runMemorySearch: %v", err)
		}
	})
	if !strings.Contains(out, "total: 1\n") {
		t.Errorf("--axi carries no total:\n%s", out)
	}
}

func TestMemorySearchJSONKeepsItsBareArray(t *testing.T) {
	memoryWorkspace(t)

	out := captureStdout(t, func() {
		if err := runMemorySearch(context.Background(), memorySearchOpts{query: "authentication tokens", topK: 5, jsonOutput: true}); err != nil {
			t.Fatalf("runMemorySearch: %v", err)
		}
	})
	if !strings.HasPrefix(out, "[") || strings.Contains(out, `"total"`) {
		t.Errorf("--json is no longer a bare array:\n%s", out)
	}
}

// ===== multisearch =====

// TestMultisearchZeroResultsInPlainAndAXI: plain already printed the totals;
// its empty line said nothing about what was searched, and --axi had no
// message at all.
func TestMultisearchZeroResultsInPlainAndAXI(t *testing.T) {
	indexedWorkspace(t)
	queries := []string{"authenticate", "invoice"}
	opts := multisearchOpts{topK: 5, threshold: 0.99}

	plain := captureStdout(t, func() {
		if err := runMultisearch(context.Background(), queries, opts); err != nil {
			t.Fatalf("runMultisearch: %v", err)
		}
	})
	if !strings.Contains(plain, "Multisearch: 2 queries, 0 results\n") {
		t.Errorf("plain output lacks the totals:\n%s", plain)
	}
	if !strings.Contains(plain, "No results found for any of the 2 queries.\n") {
		t.Errorf("plain zero-result line is wrong:\n%s", plain)
	}

	withAXI(t)
	axi := captureStdout(t, func() {
		if err := runMultisearch(context.Background(), queries, opts); err != nil {
			t.Fatalf("runMultisearch: %v", err)
		}
	})
	for _, want := range []string{"total_results: 0\n", "message: No results found for any of the 2 queries.\n"} {
		if !strings.Contains(axi, want) {
			t.Errorf("--axi zero-result output lacks %q:\n%s", want, axi)
		}
	}
}

// TestMultisearchGroupedPlainDoesNotClaimZero: with --output by_query the
// blended list is empty by design, and the old check on it printed "No results
// found." under a header that counted results.
func TestMultisearchGroupedPlainDoesNotClaimZero(t *testing.T) {
	indexedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runMultisearch(context.Background(), []string{"authenticate token"},
			multisearchOpts{topK: 5, outputFormat: "by_query"}); err != nil {
			t.Fatalf("runMultisearch: %v", err)
		}
	})
	if strings.Contains(out, "No results found") {
		t.Errorf("grouped output with results claimed none:\n%s", out)
	}
}

// TestMultisearchJSONHasNoMessage: --json keeps its schema.
func TestMultisearchJSONHasNoMessage(t *testing.T) {
	indexedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runMultisearch(context.Background(), []string{"authenticate"},
			multisearchOpts{topK: 5, threshold: 0.99, jsonOutput: true}); err != nil {
			t.Fatalf("runMultisearch: %v", err)
		}
	})
	if strings.Contains(out, `"message"`) {
		t.Errorf("--json gained a message key:\n%s", out)
	}
}

// ===== callers / refs on zero edges =====

// pythonSource defines a function the Python chunker indexes. No Python
// chunker records call edges, so `callers` over it can only ever be empty.
const pythonSource = `def validate_token(token):
    return token is not None


def handler(token):
    return validate_token(token)
`

// mixedWorkspace indexes a Go file with call edges and a Python file without.
func mixedWorkspace(t *testing.T) {
	t.Helper()
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), callGraphSource)
	writeFile(t, filepath.Join(dir, "auth.py"), pythonSource)
	captureStdout(t, func() {
		if err := runIndex(context.Background(), ".", indexOpts{skipCalibration: true}); err != nil {
			t.Fatalf("runIndex: %v", err)
		}
	})
}

// TestZeroEdgesForANonGoSymbolSayNotIndexedForEdges is the README's sharpest
// live instance: callers of a Python function used to print "No callers of
// validate_token (the symbol may not be indexed)", which reads as "none
// exist" even though the symbol is indexed and simply has no edges recorded.
func TestZeroEdgesForANonGoSymbolSayNotIndexedForEdges(t *testing.T) {
	mixedWorkspace(t)

	for name, run := range map[string]func() error{
		"callers": func() error { return runCallers(context.Background(), "validate_token", false, false) },
		"refs":    func() error { return runRefs(context.Background(), "handler", false, false) },
	} {
		out := captureStdout(t, func() {
			if err := run(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		})
		if !strings.Contains(out, "not indexed for edges") {
			t.Errorf("%s over a Python symbol does not say it is not indexed for edges:\n%s", name, out)
		}
		if strings.Contains(out, "symbol not indexed") {
			t.Errorf("%s claims an indexed Python symbol is not indexed:\n%s", name, out)
		}
	}
}

// TestZeroEdgesForAnUnknownSymbolSaySymbolNotIndexed: no chunk carries the
// name, which is a different answer from "indexed but no edges".
func TestZeroEdgesForAnUnknownSymbolSaySymbolNotIndexed(t *testing.T) {
	mixedWorkspace(t)

	for name, run := range map[string]func() error{
		"callers": func() error { return runCallers(context.Background(), "NoSuchSymbol", false, false) },
		"refs":    func() error { return runRefs(context.Background(), "NoSuchSymbol", false, false) },
	} {
		out := captureStdout(t, func() {
			if err := run(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		})
		if !strings.Contains(out, "symbol not indexed") {
			t.Errorf("%s over an unknown symbol does not say it is not indexed:\n%s", name, out)
		}
	}
}

// TestZeroCallersOfAGoSymbolIsAGenuineNone: Go records edges, so an empty
// answer there does mean none were found, and must not be hedged into the
// other two answers.
func TestZeroCallersOfAGoSymbolIsAGenuineNone(t *testing.T) {
	mixedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runCallers(context.Background(), "Caller", false, false); err != nil {
			t.Fatalf("runCallers: %v", err)
		}
	})
	if out != "No callers of Caller recorded in the call graph (0 callers).\n" {
		t.Errorf("zero callers of a Go symbol printed %q", out)
	}
}

// TestZeroEdgesInAXICarryTheStatus: --axi is what an agent reads, so the
// distinction has to be a field it can branch on, not only prose.
func TestZeroEdgesInAXICarryTheStatus(t *testing.T) {
	mixedWorkspace(t)
	withAXI(t)

	cases := []struct {
		symbol string
		status string
	}{
		{"validate_token", "status: not_indexed_for_edges\n"},
		{"NoSuchSymbol", "status: symbol_not_indexed\n"},
		{"Caller", "status: none_recorded\n"},
	}
	for _, c := range cases {
		out := captureStdout(t, func() {
			if err := runCallers(context.Background(), c.symbol, false, false); err != nil {
				t.Fatalf("runCallers %s: %v", c.symbol, err)
			}
		})
		if !strings.Contains(out, c.status) || !strings.Contains(out, "message:") || !strings.Contains(out, "count: 0\n") {
			t.Errorf("--axi callers %s lacks %q, a message or the count:\n%s", c.symbol, c.status, out)
		}
	}

	refsOut := captureStdout(t, func() {
		if err := runRefs(context.Background(), "handler", false, false); err != nil {
			t.Fatalf("runRefs: %v", err)
		}
	})
	if !strings.Contains(refsOut, "status: not_indexed_for_edges\n") {
		t.Errorf("--axi refs over a Python symbol lacks the status:\n%s", refsOut)
	}
}

// TestZeroEdgesJSONKeepsItsSchema: --json gains no key (board decision,
// 2026-10-31); the diagnosis is --axi and plain only.
func TestZeroEdgesJSONKeepsItsSchema(t *testing.T) {
	mixedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runCallers(context.Background(), "validate_token", true, false); err != nil {
			t.Fatalf("runCallers: %v", err)
		}
	})
	want := "{\n  \"callers\": [],\n  \"count\": 0,\n  \"symbol\": \"validate_token\"\n}\n"
	if out != want {
		t.Errorf("--json changed:\n got %q\nwant %q", out, want)
	}
}
