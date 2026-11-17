package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/v2/pkg/output"
)

// These tests defend EST-6878 finding 3: docs/axi-conformance.md row 9 says
// the missing-index error carries a help list, but only search (root.go) and
// index-status wrapped their error with withHelp. callers, refs, the memory
// commands, index-update and collection delete returned a bare error, so an
// agent that hit a missing index got no next step. Memory commands get their
// own wording: their store is created by `memory store`, not `index`.

// assertMissingIndexHelp checks the error a command returns with no index:
// under TOON it carries a help list naming the fix; under plain/JSON it does
// not grow a new key (the help is an AXI-only disclosure, like every other).
func assertMissingIndexHelp(t *testing.T, err error, wantPhrase string) {
	t.Helper()
	if err == nil {
		t.Fatal("no error for a missing index")
	}
	if !strings.Contains(err.Error(), "semantic index not found") {
		t.Fatalf("error = %v, want the missing-index message", err)
	}
	for axi, want := range map[bool]bool{true: true, false: false} {
		var sb strings.Builder
		f := output.New(false, false, &sb)
		f.AXI = axi
		f.PrintError(err)
		if got := strings.Contains(sb.String(), "help["); got != want {
			t.Errorf("axi=%v: help present = %v, want %v:\n%s", axi, got, want, sb.String())
		}
		if axi && !strings.Contains(sb.String(), wantPhrase) {
			t.Errorf("axi help does not name %q:\n%s", wantPhrase, sb.String())
		}
	}
}

// TestCallersMissingIndexErrorCarriesHelp: callers answers from the index, so
// a missing index must point at indexing.
func TestCallersMissingIndexErrorCarriesHelp(t *testing.T) {
	newWorkspace(t)
	err := runCallers(context.Background(), "Anything", false, false, false)
	assertMissingIndexHelp(t, err, "llm-semantic index <path>")
}

// TestRefsMissingIndexErrorCarriesHelp: same for refs, which shares the
// storage opener with callers.
func TestRefsMissingIndexErrorCarriesHelp(t *testing.T) {
	newWorkspace(t)
	err := runRefs(context.Background(), "Anything", false, false, false)
	assertMissingIndexHelp(t, err, "llm-semantic index <path>")
}

// TestMemoryCommandsMissingIndexErrorCarriesHelp: every memory command that
// needs the store points at `memory store` (its own wording), not `index`.
func TestMemoryCommandsMissingIndexErrorCarriesHelp(t *testing.T) {
	newWorkspace(t)
	for name, run := range map[string]func() error{
		"memory search": func() error {
			return runMemorySearch(context.Background(), memorySearchOpts{query: "q", topK: 5})
		},
		"memory list": func() error {
			return runMemoryList(context.Background(), memoryListOpts{limit: 50})
		},
		"memory stats": func() error {
			return runMemoryStats(context.Background(), memoryStatsOpts{})
		},
		"memory promote": func() error {
			return runMemoryPromote(context.Background(), memoryPromoteOpts{id: "mem-x", target: "CLAUDE.md"})
		},
		"memory delete": func() error {
			return runMemoryDelete(context.Background(), memoryDeleteOpts{id: "mem-x"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			assertMissingIndexHelp(t, run(), "llm-semantic memory store")
		})
	}
}

// TestIndexUpdateMissingIndexErrorCarriesHelp: an incremental update with no
// index must point at building one.
func TestIndexUpdateMissingIndexErrorCarriesHelp(t *testing.T) {
	newWorkspace(t)
	err := runIndexUpdate(context.Background(), ".", updateOpts{})
	assertMissingIndexHelp(t, err, "llm-semantic index <path>")
}

// TestCollectionDeleteMissingIndexErrorCarriesHelp: a forced delete with no
// index must point at indexing too, not fail bare.
func TestCollectionDeleteMissingIndexErrorCarriesHelp(t *testing.T) {
	newWorkspace(t)
	err := runCollectionDelete(context.Background(), collectionDeleteOpts{domain: "code", force: true})
	assertMissingIndexHelp(t, err, "llm-semantic index <path>")
}
