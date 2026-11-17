package commands

import (
	"context"
	"strings"
	"testing"
)

// These tests defend EST-6878 findings 1 and 2: the default TOON output of
// `memory list` and `memory stats` must meet the AXI shape the README and
// docs/axi-conformance.md promise for every command — a minimal row, a stated
// total, and a definitive empty state. Before the fix, `memory list` printed
// the raw 8-field entry slice (and `null` over an empty store) and
// `memory stats` printed 8-field rows with a bare `results[0]:` when empty.
// --json keeps its pre-AXI schema (the standing exception in the conformance
// scope note), pinned here so the exception stays deliberate.

// ===== memory list =====

// TestMemoryListAXIDefaultIsShapedAndStatesTotal: the default TOON list row
// carries id, question, status and the cut answer — four fields — and the
// payload states its total, like memory search.
func TestMemoryListAXIDefaultIsShapedAndStatesTotal(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t,
		newMemory("mem-1", "First question?", "First answer"),
		newMemory("mem-2", "Second question?", "Second answer"),
	)
	withAXI(t)

	out := captureStdout(t, func() {
		if err := runMemoryList(context.Background(), memoryListOpts{limit: 50}); err != nil {
			t.Fatalf("runMemoryList: %v", err)
		}
	})

	if !strings.Contains(out, "total: 2\n") {
		t.Errorf("default TOON memory list does not state total: 2:\n%s", out)
	}
	assertFieldSet(t, toonHeaderFields(t, out, "results"), []string{"id", "question", "status", "answer"}, "default memory list row")
	for _, absent := range []string{"created_at", "updated_at", "occurrences", "source"} {
		if strings.Contains(out, absent) {
			t.Errorf("default memory list row still carries %s:\n%s", absent, out)
		}
	}
}

// TestMemoryListAXICutsLongAnswerAndFullRestoresIt: the answer is cut with a
// size hint at the same 200-character limit as search content, and --full
// restores the whole answer and every entry field.
func TestMemoryListAXICutsLongAnswerAndFullRestoresIt(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	longAnswer := strings.Repeat("x", 250)
	seedMemories(t, newMemory("mem-1", "A question?", longAnswer))
	withAXI(t)

	cut := captureStdout(t, func() {
		if err := runMemoryList(context.Background(), memoryListOpts{limit: 50}); err != nil {
			t.Fatalf("runMemoryList: %v", err)
		}
	})
	if !strings.Contains(cut, "… (50 more chars, use --full for all)") {
		t.Errorf("the long answer is not cut with a size hint:\n%s", cut)
	}
	if strings.Contains(cut, longAnswer) {
		t.Errorf("the default row carries the whole 250-char answer:\n%s", cut)
	}

	full := captureStdout(t, func() {
		if err := runMemoryList(context.Background(), memoryListOpts{limit: 50, full: true}); err != nil {
			t.Fatalf("runMemoryList --full: %v", err)
		}
	})
	if !strings.Contains(full, longAnswer) {
		t.Errorf("--full did not restore the whole answer:\n%s", full)
	}
	// A uniform TOON row list names its fields once, in the header.
	assertFieldSet(t, toonHeaderFields(t, full, "results"),
		[]string{"id", "question", "answer", "status", "occurrences", "created_at", "updated_at", "source"},
		"--full memory list row")
	if !strings.Contains(full, "total: 1\n") {
		t.Errorf("--full lost the total:\n%s", full)
	}
}

// TestMemoryListAXIEmptyStoreSaysStoreEmpty: an empty store must not print
// `null` — it says the store is empty, with a total of 0, an empty result list
// and a next step, so an agent can branch on it.
func TestMemoryListAXIEmptyStoreSaysStoreEmpty(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t) // creates the index with no rows
	withAXI(t)

	out := captureStdout(t, func() {
		if err := runMemoryList(context.Background(), memoryListOpts{limit: 50}); err != nil {
			t.Fatalf("runMemoryList: %v", err)
		}
	})

	if strings.TrimSpace(out) == "null" {
		t.Fatalf("empty store printed null:\n%s", out)
	}
	for _, want := range []string{"total: 0", "status: store_empty", "results[0]:", "memory store is empty", "help["} {
		if !strings.Contains(out, want) {
			t.Errorf("empty-store list missing %q:\n%s", want, out)
		}
	}
}

// TestMemoryListAXIFilterExcludingEverythingIsNoMatch: a filter that excludes
// every entry is not an empty store, and must not claim the store is empty.
func TestMemoryListAXIFilterExcludingEverythingIsNoMatch(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-pending", "Pending?", "Yes"))
	withAXI(t)

	out := captureStdout(t, func() {
		if err := runMemoryList(context.Background(), memoryListOpts{limit: 50, status: "promoted"}); err != nil {
			t.Fatalf("runMemoryList: %v", err)
		}
	})

	if !strings.Contains(out, "status: no_match") {
		t.Errorf("filter-empty list is not no_match:\n%s", out)
	}
	if strings.Contains(out, "store_empty") {
		t.Errorf("a non-empty store was reported as store_empty:\n%s", out)
	}
	if !strings.Contains(out, "total: 0") {
		t.Errorf("filter-empty list does not state total: 0:\n%s", out)
	}
}

// TestMemoryListAXIMinStatesTotal: --min keeps its compact rows, but the TOON
// payload states a total like every other TOON answer (principle 4).
func TestMemoryListAXIMinStatesTotal(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "A question?", "An answer nobody needs in min mode"))
	withAXI(t)

	out := captureStdout(t, func() {
		if err := runMemoryList(context.Background(), memoryListOpts{limit: 50, minOutput: true}); err != nil {
			t.Fatalf("runMemoryList --min: %v", err)
		}
	})

	if !strings.Contains(out, "total: 1\n") {
		t.Errorf("--min TOON list does not state total: 1:\n%s", out)
	}
	assertFieldSet(t, toonHeaderFields(t, out, "results"), []string{"id", "q", "status"}, "--min memory list row")
	if strings.Contains(out, "answer") {
		t.Errorf("--min list carried the answer:\n%s", out)
	}
}

// TestMemoryListJSONKeepsTheRawEntries pins the declared --json exception:
// the pre-AXI schema keeps every entry field, untruncated, as a bare array.
func TestMemoryListJSONKeepsTheRawEntries(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "A question?", "The whole answer"))

	out := captureStdout(t, func() {
		if err := runMemoryList(context.Background(), memoryListOpts{limit: 50, jsonOutput: true}); err != nil {
			t.Fatalf("runMemoryList --json: %v", err)
		}
	})

	var got []map[string]any
	decodeJSON(t, out, &got)
	if len(got) != 1 {
		t.Fatalf("--json list = %v, want one entry", got)
	}
	for _, key := range []string{"id", "question", "answer", "status", "occurrences", "created_at", "updated_at", "source"} {
		if _, ok := got[0][key]; !ok {
			t.Errorf("--json entry lost its pre-AXI field %q: %v", key, got[0])
		}
	}
	if got[0]["answer"] != "The whole answer" {
		t.Errorf("--json answer was cut: %v", got[0]["answer"])
	}
}

// ===== memory stats =====

// TestMemoryStatsAXIDefaultIsShapedAndStatesTotal: the default TOON stats row
// carries id, question, retrieval_count and avg_score — four fields — while
// the payload keeps its pre-computed summary counts and states a total.
func TestMemoryStatsAXIDefaultIsShapedAndStatesTotal(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "A question?", "An answer"))
	seedRetrievals(t, "mem-1", "a query", 0.5, 2)
	withAXI(t)

	out := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{}); err != nil {
			t.Fatalf("runMemoryStats: %v", err)
		}
	})

	for _, want := range []string{"total_memories: 1", "pending: 1", "promoted: 0", "total: 1\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("default TOON stats missing %q:\n%s", want, out)
		}
	}
	assertFieldSet(t, toonHeaderFields(t, out, "results"), []string{"id", "question", "retrieval_count", "avg_score"}, "default memory stats row")
	for _, absent := range []string{"last_retrieved:", "created:", "tags"} {
		if strings.Contains(out, absent) {
			t.Errorf("default stats row still carries %s:\n%s", absent, out)
		}
	}
}

// TestMemoryStatsAXIFullRestoresEveryField: --full is the escape hatch — the
// row keeps every field the pre-AXI schema carried.
func TestMemoryStatsAXIFullRestoresEveryField(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "A question?", "An answer"))
	seedRetrievals(t, "mem-1", "a query", 0.5, 1)
	withAXI(t)

	out := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{full: true}); err != nil {
			t.Fatalf("runMemoryStats --full: %v", err)
		}
	})

	for _, want := range []string{"last_retrieved:", "created:", "status:", "total_memories: 1", "total: 1\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("--full stats missing %q:\n%s", want, out)
		}
	}
}

// TestMemoryStatsAXIEmptyStoreSaysStoreEmpty: an empty store must not print a
// bare `results[0]:` with no status — it says the store is empty.
func TestMemoryStatsAXIEmptyStoreSaysStoreEmpty(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t) // creates the index with no rows
	withAXI(t)

	out := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{}); err != nil {
			t.Fatalf("runMemoryStats: %v", err)
		}
	})

	for _, want := range []string{"total_memories: 0", "status: store_empty", "results[0]:", "memory store is empty", "help["} {
		if !strings.Contains(out, want) {
			t.Errorf("empty-store stats missing %q:\n%s", want, out)
		}
	}
}

// TestMemoryStatsAXINoRetrievalsIsHonest: a store with entries but no
// retrievals has no stats to show. That is not an empty store, and the message
// must not claim no memories exist when they do.
func TestMemoryStatsAXINoRetrievalsIsHonest(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "A question?", "An answer"))
	withAXI(t)

	out := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{}); err != nil {
			t.Fatalf("runMemoryStats: %v", err)
		}
	})

	if !strings.Contains(out, "status: no_match") {
		t.Errorf("stats with no retrievals is not no_match:\n%s", out)
	}
	if strings.Contains(out, "store_empty") {
		t.Errorf("a store with an entry was reported as store_empty:\n%s", out)
	}
	if strings.Contains(out, "No memories found") {
		t.Errorf("stats claimed no memories exist while the store holds one:\n%s", out)
	}
	if !strings.Contains(out, "retrieval") {
		t.Errorf("the empty-stats message does not say what is missing:\n%s", out)
	}
}

// TestMemoryStatsAXIFilterExcludingEverythingIsNoMatch: a filter that excludes
// every entry is not an empty store, and the message names the filters.
func TestMemoryStatsAXIFilterExcludingEverythingIsNoMatch(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "A question?", "An answer"))
	seedRetrievals(t, "mem-1", "a query", 0.5, 1)
	withAXI(t)

	out := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{minRetrievals: 100}); err != nil {
			t.Fatalf("runMemoryStats: %v", err)
		}
	})

	if !strings.Contains(out, "status: no_match") {
		t.Errorf("filter-empty stats is not no_match:\n%s", out)
	}
	if strings.Contains(out, "store_empty") {
		t.Errorf("a store with an entry was reported as store_empty:\n%s", out)
	}
	if !strings.Contains(out, "No memories match the filters.") {
		t.Errorf("filter-empty stats does not name the filters:\n%s", out)
	}
	if !strings.Contains(out, "total: 0") || !strings.Contains(out, "total_memories: 1") {
		t.Errorf("filter-empty stats lost its totals:\n%s", out)
	}
}

// TestMemoryStatsJSONKeepsThePreAXISchema pins the declared --json exception:
// the summary object and its full 8-field rows are unchanged.
func TestMemoryStatsJSONKeepsThePreAXISchema(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	seedMemories(t, newMemory("mem-1", "A question?", "An answer"))
	seedRetrievals(t, "mem-1", "a query", 0.5, 1)

	out := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{jsonOutput: true}); err != nil {
			t.Fatalf("runMemoryStats --json: %v", err)
		}
	})

	var got struct {
		TotalMemories int              `json:"total_memories"`
		Pending       int              `json:"pending"`
		Promoted      int              `json:"promoted"`
		Results       []map[string]any `json:"results"`
	}
	decodeJSON(t, out, &got)
	if got.TotalMemories != 1 || got.Pending != 1 || len(got.Results) != 1 {
		t.Fatalf("--json stats summary changed: %+v", got)
	}
	for _, key := range []string{"id", "question", "retrieval_count", "last_retrieved", "avg_score", "status", "created", "tags"} {
		if _, ok := got.Results[0][key]; !ok {
			t.Errorf("--json stats row lost its pre-AXI field %q: %v", key, got.Results[0])
		}
	}
}
