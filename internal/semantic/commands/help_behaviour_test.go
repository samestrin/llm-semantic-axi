package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/pkg/output"
)

// Epic 05.1 T3. --axi output suggests a next step in a help list; --json and
// plain output carry no new key.

// TestSearchAXIResultsCarryHelp: a hit list with no next step leaves an agent
// to guess the follow-up command.
func TestSearchAXIResultsCarryHelp(t *testing.T) {
	indexedWorkspace(t)
	withAXI(t)
	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate token", searchOpts{topK: 1}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})
	if !strings.Contains(out, "help[") || !strings.Contains(out, "llm-semantic callers") {
		t.Errorf("--axi results carry no help list:\n%s", out)
	}
}

// TestSearchAXIZeroResultsCarryHelp: an empty answer must say how to widen it.
func TestSearchAXIZeroResultsCarryHelp(t *testing.T) {
	indexedWorkspace(t)
	withAXI(t)
	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate", searchOpts{topK: 5, threshold: 0.99}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})
	if !strings.Contains(out, "help[") || !strings.Contains(out, "--threshold 0") {
		t.Errorf("--axi zero-result output carries no help list:\n%s", out)
	}
}

// TestSearchJSONAndPlainHaveNoHelp: --json's schema is fixed (board, 2026-10-31).
func TestSearchJSONAndPlainHaveNoHelp(t *testing.T) {
	indexedWorkspace(t)
	for _, opts := range []searchOpts{{topK: 1, jsonOutput: true}, {topK: 1}, {topK: 5, threshold: 0.99, jsonOutput: true}} {
		out := captureStdout(t, func() {
			if err := runSearch(context.Background(), "authenticate token", opts); err != nil {
				t.Fatalf("runSearch: %v", err)
			}
		})
		if strings.Contains(out, `"help"`) || strings.Contains(out, "help[") {
			t.Errorf("%+v output carries help:\n%s", opts, out)
		}
	}
}

// TestSearchMissingIndexAXIErrorCarriesHelp: the failure of a search with no
// index must point at indexing, and only under --axi.
func TestSearchMissingIndexAXIErrorCarriesHelp(t *testing.T) {
	t.Chdir(t.TempDir())
	_, _, err := initSearchComponents(context.Background(), createStorage)
	if err == nil {
		t.Fatal("no error for a missing index")
	}
	for axi, want := range map[bool]bool{true: true, false: false} {
		var sb strings.Builder
		f := output.New(false, false, &sb)
		f.AXI = axi
		f.PrintError(err)
		if got := strings.Contains(sb.String(), "llm-semantic index <path>"); got != want {
			t.Errorf("axi=%v: help present = %v, want %v:\n%s", axi, got, want, sb.String())
		}
	}
}

// TestIndexStatusMissingIndexAXIErrorCarriesHelp: same for index-status.
func TestIndexStatusMissingIndexAXIErrorCarriesHelp(t *testing.T) {
	t.Chdir(t.TempDir())
	err := runIndexStatus(context.Background(), false)
	if err == nil {
		t.Fatal("no error for a missing index")
	}
	var sb strings.Builder
	f := output.New(false, false, &sb)
	f.AXI = true
	f.PrintError(err)
	if !strings.Contains(sb.String(), "help[") {
		t.Errorf("no help in --axi error:\n%s", sb.String())
	}
}

// TestIndexStatusAXICarriesHelpJSONDoesNot: status over a real index.
func TestIndexStatusAXICarriesHelpJSONDoesNot(t *testing.T) {
	indexedWorkspace(t)
	js := captureStdout(t, func() {
		if err := runIndexStatus(context.Background(), true); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(js, `"help"`) {
		t.Errorf("--json status has help:\n%s", js)
	}
	withAXI(t)
	ax := captureStdout(t, func() {
		if err := runIndexStatus(context.Background(), false); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(ax, "help[") {
		t.Errorf("--axi status has no help:\n%s", ax)
	}
}

// TestStatusHelpPointsAnEmptyIndexAtIndexing: an index holding no files or no
// chunks is not a working one, so the help must say to build it, not to search.
func TestStatusHelpPointsAnEmptyIndexAtIndexing(t *testing.T) {
	withAXI(t)
	for _, s := range []indexStatusJSON{{Indexed: true}, {Indexed: true, FilesIndexed: 3}, {Indexed: true, ChunksTotal: 3}} {
		got, ok := withStatusHelp(s).(indexStatusAXI)
		if !ok || len(got.Help) != 1 || !strings.Contains(got.Help[0], "llm-semantic index <path>") {
			t.Errorf("%+v help = %+v", s, got.Help)
		}
	}
	healthy := withStatusHelp(indexStatusJSON{Indexed: true, FilesIndexed: 1, ChunksTotal: 2}).(indexStatusAXI)
	if strings.Contains(strings.Join(healthy.Help, " "), "populate") {
		t.Errorf("healthy index told to populate: %v", healthy.Help)
	}
}

// TestMemorySearchHelpInAXIOnly: memory search results and zero-results carry
// help under --axi; --json keeps its bare array.
func TestMemorySearchHelpInAXIOnly(t *testing.T) {
	memoryWorkspace(t)
	run := func(opts memorySearchOpts) string {
		return captureStdout(t, func() {
			if err := runMemorySearch(context.Background(), opts); err != nil {
				t.Fatalf("runMemorySearch: %v", err)
			}
		})
	}
	js := run(memorySearchOpts{query: "authentication tokens", topK: 5, jsonOutput: true})
	if strings.Contains(js, `"help"`) {
		t.Errorf("--json has help:\n%s", js)
	}
	withAXI(t)
	if out := run(memorySearchOpts{query: "authentication tokens", topK: 5}); !strings.Contains(out, "help[") || !strings.Contains(out, "browse stored entries") {
		t.Errorf("--axi results lack help:\n%s", out)
	}
	if out := run(memorySearchOpts{query: "authentication", topK: 5, threshold: 0.99}); !strings.Contains(out, "help[") || !strings.Contains(out, "--threshold 0") {
		t.Errorf("--axi zero-result lacks help:\n%s", out)
	}
}

// TestMultisearchHelpInAXIOnly: results and zero-results carry help under
// --axi; --json has no help key.
func TestMultisearchHelpInAXIOnly(t *testing.T) {
	indexedWorkspace(t)
	queries := []string{"authenticate token", "validate"}
	run := func(opts multisearchOpts) string {
		return captureStdout(t, func() {
			if err := runMultisearch(context.Background(), queries, opts); err != nil {
				t.Fatalf("runMultisearch: %v", err)
			}
		})
	}
	if js := run(multisearchOpts{topK: 5, jsonOutput: true}); strings.Contains(js, `"help"`) {
		t.Errorf("--json has help:\n%s", js)
	}
	withAXI(t)
	if out := run(multisearchOpts{topK: 5}); !strings.Contains(out, "help[") || !strings.Contains(out, "callers") || strings.Contains(out, "message:") {
		t.Errorf("--axi results wrong:\n%s", out)
	}
	if out := run(multisearchOpts{topK: 5, threshold: 0.99}); !strings.Contains(out, "help[") || !strings.Contains(out, "--threshold 0") || !strings.Contains(out, "message:") {
		t.Errorf("--axi zero-result wrong:\n%s", out)
	}
}
