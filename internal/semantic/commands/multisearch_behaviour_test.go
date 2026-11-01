package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// ===== runMultisearch =====

// TestMultisearchMergesQueriesIntoOneRankedList pins the default (blended)
// output: one list, boosted scores, and the queries each result matched.
func TestMultisearchMergesQueriesIntoOneRankedList(t *testing.T) {
	indexedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runMultisearch(context.Background(),
			[]string{"authenticate bearer token", "render invoice billing"},
			multisearchOpts{topK: 10}); err != nil {
			t.Fatalf("runMultisearch: %v", err)
		}
	})

	if !strings.Contains(out, "Multisearch: 2 queries") {
		t.Errorf("the query count is wrong:\n%s", out)
	}
	if !strings.Contains(out, "Authenticate") || !strings.Contains(out, "RenderInvoice") {
		t.Errorf("both queries' matches should appear:\n%s", out)
	}
	if !strings.Contains(out, "boosted:") {
		t.Errorf("the boosted score is missing:\n%s", out)
	}
	if !strings.Contains(out, "Matched:") {
		t.Errorf("the matched-query list is missing:\n%s", out)
	}
}

// TestMultisearchJSONCarriesTheTotals pins the machine shape of the blended
// result.
func TestMultisearchJSONCarriesTheTotals(t *testing.T) {
	indexedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runMultisearch(context.Background(),
			[]string{"authenticate token", "invoice billing"},
			multisearchOpts{topK: 10, jsonOutput: true}); err != nil {
			t.Fatalf("runMultisearch: %v", err)
		}
	})

	var payload struct {
		TotalQueries int `json:"total_queries"`
		TotalResults int `json:"total_results"`
		Results      []struct {
			Chunk struct {
				Name string `json:"name"`
			} `json:"chunk"`
		} `json:"results"`
	}
	decodeJSON(t, out, &payload)
	if payload.TotalQueries != 2 {
		t.Errorf("total_queries = %d, want 2", payload.TotalQueries)
	}
	if payload.TotalResults == 0 || len(payload.Results) == 0 {
		t.Fatalf("--json returned no results:\n%s", out)
	}
}

// TestMultisearchMinimalDropsTheChunkBody: the minimal shape exists so an agent
// can read many results cheaply, so it must not carry the full chunk.
func TestMultisearchMinimalDropsTheChunkBody(t *testing.T) {
	indexedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runMultisearch(context.Background(),
			[]string{"authenticate token"},
			multisearchOpts{topK: 10, minOutput: true}); err != nil {
			t.Fatalf("runMultisearch: %v", err)
		}
	})

	var payload struct {
		TotalQueries int              `json:"total_queries"`
		Results      []map[string]any `json:"results"`
	}
	decodeJSON(t, out, &payload)
	if payload.TotalQueries != 1 {
		t.Errorf("total_queries = %d, want 1", payload.TotalQueries)
	}
	if len(payload.Results) == 0 {
		t.Fatalf("--min returned no results:\n%s", out)
	}
	for _, key := range []string{"file", "name", "line", "score", "boosted", "matches"} {
		if _, present := payload.Results[0][key]; !present {
			t.Errorf("--min dropped %q: %v", key, payload.Results[0])
		}
	}
	if _, present := payload.Results[0]["chunk"]; present {
		t.Errorf("--min carried the whole chunk: %v", payload.Results[0])
	}
}

// TestMultisearchByQueryGroupsResults covers the by_query output format, which
// replaces the blended list with a map keyed on the query.
func TestMultisearchByQueryGroupsResults(t *testing.T) {
	indexedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runMultisearch(context.Background(),
			[]string{"authenticate bearer token", "render invoice billing"},
			multisearchOpts{topK: 10, jsonOutput: true, outputFormat: semantic.OutputByQuery}); err != nil {
			t.Fatalf("runMultisearch: %v", err)
		}
	})

	var payload struct {
		ByQuery map[string][]map[string]any `json:"by_query"`
	}
	decodeJSON(t, out, &payload)
	if len(payload.ByQuery) != 2 {
		t.Fatalf("by_query has %d keys, want one per query:\n%s", len(payload.ByQuery), out)
	}
	if _, present := payload.ByQuery["authenticate bearer token"]; !present {
		t.Errorf("by_query is not keyed on the query text: %v", payload.ByQuery)
	}
}

// TestMultisearchMinimalByQueryAndByCollection covers the two grouped branches
// of the minimal encoder, which build their own maps rather than reusing the
// blended one.
func TestMultisearchMinimalByQueryAndByCollection(t *testing.T) {
	indexedWorkspace(t)

	for _, tt := range []struct {
		name   string
		format semantic.OutputFormat
		key    string
	}{
		{"by query", semantic.OutputByQuery, "by_query"},
		{"by collection", semantic.OutputByCollection, "by_collection"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() {
				if err := runMultisearch(context.Background(),
					[]string{"authenticate bearer token"},
					multisearchOpts{topK: 10, minOutput: true, outputFormat: tt.format}); err != nil {
					t.Fatalf("runMultisearch: %v", err)
				}
			})

			var payload map[string]any
			decodeJSON(t, out, &payload)
			group, present := payload[tt.key].(map[string]any)
			if !present {
				t.Fatalf("--min %s output has no %q block:\n%s", tt.name, tt.key, out)
			}
			if len(group) == 0 {
				t.Fatalf("%q is empty:\n%s", tt.key, out)
			}
			for _, entries := range group {
				list, _ := entries.([]any)
				if len(list) == 0 {
					continue
				}
				first, _ := list[0].(map[string]any)
				for _, field := range []string{"file", "name", "line", "score", "boosted"} {
					if _, ok := first[field]; !ok {
						t.Errorf("grouped minimal entry dropped %q: %v", field, first)
					}
				}
			}
		})
	}
}

// TestMultisearchTextReportsAnEmptyResultSet: the header still prints, so the
// caller can tell an empty search from a crashed one.
func TestMultisearchTextReportsAnEmptyResultSet(t *testing.T) {
	indexedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runMultisearch(context.Background(),
			[]string{"authenticate"},
			multisearchOpts{topK: 10, threshold: 0.99}); err != nil {
			t.Fatalf("runMultisearch: %v", err)
		}
	})

	if !strings.Contains(out, "Multisearch: 1 queries, 0 results") {
		t.Errorf("the header is wrong:\n%s", out)
	}
	if !strings.Contains(out, "No results found for") {
		t.Errorf("an empty result set printed %q", out)
	}
}

// TestMultisearchWithoutAnIndexNamesTheFix keeps the diagnosis consistent with
// the single-query search.
func TestMultisearchWithoutAnIndexNamesTheFix(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))

	err := runMultisearch(context.Background(), []string{"anything"}, multisearchOpts{topK: 10})
	if err == nil {
		t.Fatal("multisearch with no index returned nil")
	}
	if !strings.Contains(err.Error(), "llm-semantic index") {
		t.Errorf("error = %v, want it to name the indexing command", err)
	}
}

// TestMultisearchSurfacesASearchFailure: with a dead embedder the queries
// cannot be embedded, and reporting zero results would look like a clean miss.
func TestMultisearchSurfacesASearchFailure(t *testing.T) {
	indexedWorkspace(t)
	t.Cleanup(SetAPIURLForTesting(newBrokenEmbedder(t).URL))

	err := runMultisearch(context.Background(), []string{"authenticate"}, multisearchOpts{topK: 10})
	if err == nil {
		t.Fatal("multisearch against a failing embedder returned nil")
	}
	if !strings.Contains(err.Error(), "multisearch failed") {
		t.Errorf("error = %v, want it wrapped as a multisearch failure", err)
	}
}

// ===== multisearchCmd validation =====

// TestMultisearchCmdValidatesItsArguments covers the four guards in front of
// the handler. Each one exists because the failure downstream is worse: an
// unbounded query list, an all-empty query set, a silently ignored format and
// a profile name that matches no collection.
func TestMultisearchCmdValidatesItsArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "too many queries",
			args: make([]string, semantic.MaxQueries+1),
			want: "multisearch supports up to",
		},
		{"only blank queries", []string{"   ", "\t"}, "non-empty query required"},
		{"unknown output format", []string{"q", "--output", "sideways"}, "invalid output format"},
		{"unknown profile", []string{"q", "--profiles", "nonsense"}, "invalid profile"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newWorkspace(t)
			args := tt.args
			if tt.name == "too many queries" {
				for i := range args {
					args[i] = "q"
				}
			}
			cmd := multisearchCmd()
			cmd.SetArgs(args)
			cmd.SetOut(&strings.Builder{})
			cmd.SetErr(&strings.Builder{})

			err := cmd.Execute()
			if err == nil {
				t.Fatalf("%s was accepted", tt.name)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}
