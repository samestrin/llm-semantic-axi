package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/v2/internal/semantic"
)

// EST-8171 item 3: memory stats counted only memories with retrieval records,
// so after one `memory store` it said total_memories: 0 while `memory list`
// said total: 1. The totals describe the store, so they are counted from it;
// the rows stay the memories that have retrieval stats.

func storeWithoutRetrievals(t *testing.T) {
	t.Helper()
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	promoted := newMemory("mem-2", "Second question?", "Second answer")
	promoted.Status = semantic.MemoryStatusPromoted
	seedMemories(t, newMemory("mem-1", "A question?", "An answer"), promoted)
}

func TestMemoryStatsTotalsCountTheStoreInTOON(t *testing.T) {
	storeWithoutRetrievals(t)
	withAXI(t)

	out := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{}); err != nil {
			t.Fatalf("runMemoryStats: %v", err)
		}
	})

	for _, want := range []string{"total_memories: 2", "pending: 1", "promoted: 1", "total: 0\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("TOON stats lacks %q:\n%s", want, out)
		}
	}
}

func TestMemoryStatsTotalsCountTheStoreInJSON(t *testing.T) {
	storeWithoutRetrievals(t)
	withPlain(t)

	var full struct {
		TotalMemories int `json:"total_memories"`
		Pending       int `json:"pending"`
		Promoted      int `json:"promoted"`
	}
	decodeJSON(t, captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{jsonOutput: true}); err != nil {
			t.Fatalf("runMemoryStats: %v", err)
		}
	}), &full)
	if full.TotalMemories != 2 || full.Pending != 1 || full.Promoted != 1 {
		t.Errorf("--json totals = %+v, want 2 / 1 / 1", full)
	}

	var min struct {
		T int `json:"t"`
		P int `json:"p"`
		M int `json:"m"`
	}
	decodeJSON(t, captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{jsonOutput: true, minOutput: true}); err != nil {
			t.Fatalf("runMemoryStats: %v", err)
		}
	}), &min)
	if min.T != 2 || min.P != 1 || min.M != 1 {
		t.Errorf("--json --min totals = %+v, want 2 / 1 / 1", min)
	}
}

// TestMemoryStatsPlainDoesNotDenyStoredMemories: "No memories found." over a
// store holding two is the same lie in the text format.
func TestMemoryStatsPlainDoesNotDenyStoredMemories(t *testing.T) {
	storeWithoutRetrievals(t)
	withPlain(t)

	out := captureStdout(t, func() {
		if err := runMemoryStats(context.Background(), memoryStatsOpts{}); err != nil {
			t.Fatalf("runMemoryStats: %v", err)
		}
	})

	if strings.Contains(out, "No memories found") {
		t.Errorf("stats denied memories the store holds:\n%s", out)
	}
	for _, want := range []string{"Total: 2 | Pending: 1 | Promoted: 1", "retrieval"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain stats lacks %q:\n%s", want, out)
		}
	}
}
