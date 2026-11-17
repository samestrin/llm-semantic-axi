package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These tests pin the true conditions behind the README's zero-result --json
// wording (EST-6829 finding 2). Verified against a built binary of d2da5e1
// with a local fake embedder:
//
//   - a dense search whose store query returns nothing prints `null` — the
//     result slice is nil, whether --path/--type matched no chunk, --threshold
//     cut every candidate, or the index is empty;
//   - reranking that removes every candidate prints `[]` — the filter reslices
//     a non-nil slice down to zero;
//   - --hybrid fusion with no surviving result prints `[]` for the same reason;
//   - --min always prints `[]` when empty, because it builds a fresh row list.
//
// The old README said `[]` only "when reranking is on", which missed hybrid
// and --min, and the operator check's dense --threshold example did not
// reproduce: dense --threshold prints null.

// TestSearchZeroJSONIsNullWhenTheStoreReturnsNothing defends the null half of
// the README claim: a dense search with nothing to return prints null, not [].
func TestSearchZeroJSONIsNullWhenTheStoreReturnsNothing(t *testing.T) {
	indexedWorkspace(t)

	for _, opts := range []searchOpts{
		{topK: 5, threshold: 0.99, jsonOutput: true},
		{topK: 5, pathFilter: "nowhere/", jsonOutput: true},
		{topK: 5, typeFilter: "class", jsonOutput: true},
	} {
		out := captureStdout(t, func() {
			if err := runSearch(context.Background(), "zzzqqq", opts); err != nil {
				t.Fatalf("runSearch %+v: %v", opts, err)
			}
		})
		if out != "null\n" {
			t.Errorf("zero-result dense --json (%+v) = %q, want null", opts, out)
		}
	}
}

// TestSearchZeroJSONIsArrayWhenHybridFusionEmptiesTheList defends the [] half:
// hybrid fusion builds a fresh result list, so an empty answer is [], not null.
func TestSearchZeroJSONIsArrayWhenHybridFusionEmptiesTheList(t *testing.T) {
	indexedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "zzzqqq", searchOpts{topK: 5, threshold: 0.99, hybrid: true, jsonOutput: true}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})
	if out != "[]\n" {
		t.Errorf("zero-result hybrid --json = %q, want []", out)
	}
}

// TestSearchZeroJSONIsArrayWhenRerankingRemovesEveryResult defends the [] half
// for the reranker: its threshold filter reslices the candidate list, so an
// empty answer is [], not null. The reranker is an in-process fake; no test
// reaches a live endpoint.
func TestSearchZeroJSONIsArrayWhenRerankingRemovesEveryResult(t *testing.T) {
	indexedWorkspace(t)
	reranker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/rerank") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req struct {
			Documents []string `json:"documents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		type result struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		}
		resp := struct {
			Results []result `json:"results"`
		}{}
		for i := range req.Documents {
			resp.Results = append(resp.Results, result{Index: i, RelevanceScore: 0.01})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(reranker.Close)
	// After indexedWorkspace cleared the developer's reranker environment,
	// point the command at the fake for this test only.
	t.Setenv("LLM_SEMANTIC_RERANKER_API_URL", reranker.URL)

	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate", searchOpts{topK: 5, threshold: 0.99, jsonOutput: true}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})
	if out != "[]\n" {
		t.Errorf("zero-result reranked --json = %q, want []", out)
	}
}

// TestSearchZeroMinIsAlwaysAnArray defends the --min clause: the minimal row
// list is built fresh, so an empty answer is [] even with no reranker.
func TestSearchZeroMinIsAlwaysAnArray(t *testing.T) {
	indexedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "zzzqqq", searchOpts{topK: 5, threshold: 0.99, minOutput: true}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})
	if out != "[]\n" {
		t.Errorf("zero-result --min = %q, want []", out)
	}
}
