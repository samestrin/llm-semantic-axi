package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// useReranker points the commands at an in-process reranker API. A failing one
// answers every call with a 400, which the client does not retry.
func useReranker(t *testing.T, failing bool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing {
			http.Error(w, "model not loaded", http.StatusBadRequest)
			return
		}
		var req struct {
			Documents []string `json:"documents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		results := make([]map[string]any, len(req.Documents))
		for i := range req.Documents {
			results[i] = map[string]any{"index": i, "relevance_score": 0.9}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LLM_SEMANTIC_RERANKER_API_URL", srv.URL)
}

// runRerankedSearch runs one search with the given output mode and returns
// stdout and stderr.
func runRerankedSearch(t *testing.T, opts searchOpts) (stdout, stderr string) {
	t.Helper()
	opts.topK = 5
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			if err := runSearch(context.Background(), "authenticate bearer token", opts); err != nil {
				t.Fatalf("runSearch: %v (a failed reranker with results must still exit 0)", err)
			}
		})
	})
	return stdout, stderr
}

// countLines counts the lines of s that contain sub.
func countLines(s, sub string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, sub) {
			n++
		}
	}
	return n
}

// TestSearchStatesRerankerFailureInTOON defends epic 35: a reranker that fails
// falls back to embedding scores, and the answer must say so with a status an
// agent can branch on and a help line, not pass for a reranked answer.
func TestSearchStatesRerankerFailureInTOON(t *testing.T) {
	indexedWorkspace(t)
	useReranker(t, true)
	withAXI(t)

	out, _ := runRerankedSearch(t, searchOpts{})

	if !strings.Contains(out, "status: reranker_failed\n") {
		t.Errorf("TOON carries no reranker_failed status:\n%s", out)
	}
	if !strings.Contains(out, "model not loaded") {
		t.Errorf("TOON does not name why the reranker failed:\n%s", out)
	}
	if !strings.Contains(out, "--no-rerank") {
		t.Errorf("TOON help gives no next step for the failed reranker:\n%s", out)
	}
	if !strings.Contains(out, "Authenticate") {
		t.Errorf("the embedding fallback results are missing:\n%s", out)
	}
}

// TestSearchStatesRerankerFailureInPlain: the plain answer carries one line
// naming the failure, after the results it qualifies.
func TestSearchStatesRerankerFailureInPlain(t *testing.T) {
	indexedWorkspace(t)
	useReranker(t, true)
	withPlain(t)

	out, _ := runRerankedSearch(t, searchOpts{})

	if got := countLines(out, "reranker failed: "); got != 1 {
		t.Errorf("plain output has %d reranker-failed lines, want 1:\n%s", got, out)
	}
	if !strings.Contains(out, "model not loaded") {
		t.Errorf("plain output does not name why the reranker failed:\n%s", out)
	}
	if !strings.Contains(out, "Authenticate") {
		t.Errorf("the embedding fallback results are missing:\n%s", out)
	}
}

// TestSearchStatesRerankerFailureBesideJSON: --json stays a bare array (board
// decision 2026-10-31), so the failure goes to stderr the way the staleness
// line does, and stdout still decodes as the result list.
func TestSearchStatesRerankerFailureBesideJSON(t *testing.T) {
	indexedWorkspace(t)
	useReranker(t, true)
	withPlain(t)

	for _, opts := range []searchOpts{{jsonOutput: true}, {minOutput: true}} {
		t.Run(fmt.Sprintf("json=%v,min=%v", opts.jsonOutput, opts.minOutput), func(t *testing.T) {
			out, errOut := runRerankedSearch(t, opts)

			var results []map[string]any
			decodeJSON(t, out, &results)
			if len(results) == 0 {
				t.Errorf("--json returned no fallback results:\n%s", out)
			}
			if got := countLines(errOut, "reranker failed: "); got != 1 {
				t.Errorf("stderr has %d reranker-failed lines, want 1:\n%s", got, errOut)
			}
			if !strings.Contains(errOut, "model not loaded") {
				t.Errorf("stderr does not name why the reranker failed:\n%s", errOut)
			}
		})
	}
}

// TestSearchHealthyRerankStatesNothing guards the other direction: a reranker
// that worked adds no status, no help line and no failure line in any mode.
func TestSearchHealthyRerankStatesNothing(t *testing.T) {
	indexedWorkspace(t)
	useReranker(t, false)

	withAXI(t)
	toon, toonErr := runRerankedSearch(t, searchOpts{})
	withPlain(t)
	plain, plainErr := runRerankedSearch(t, searchOpts{})
	jsonOut, jsonErr := runRerankedSearch(t, searchOpts{jsonOutput: true})

	if !strings.Contains(toon, "Authenticate") || !strings.Contains(plain, "Authenticate") {
		t.Fatalf("the reranked search returned nothing:\n%s\n%s", toon, plain)
	}
	if strings.Contains(toon, "status:") || strings.Contains(toon, "--no-rerank") {
		t.Errorf("TOON reports a status for a healthy rerank:\n%s", toon)
	}
	for name, s := range map[string]string{"toon stderr": toonErr, "plain": plain, "plain stderr": plainErr, "json": jsonOut, "json stderr": jsonErr} {
		if strings.Contains(strings.ToLower(s), "reranker failed") {
			t.Errorf("%s reports a reranker failure for a healthy rerank:\n%s", name, s)
		}
	}
	var results []map[string]any
	decodeJSON(t, jsonOut, &results)
}

// TestSearchPrefilterStatesRerankerFailure covers the --prefilter path, which
// reaches the reranker through PrefilterSearch rather than Search.
func TestSearchPrefilterStatesRerankerFailure(t *testing.T) {
	indexedWorkspace(t)
	useReranker(t, true)
	withAXI(t)

	out, _ := runRerankedSearch(t, searchOpts{prefilter: true})

	if !strings.Contains(out, "status: reranker_failed\n") {
		t.Errorf("--prefilter TOON carries no reranker_failed status:\n%s", out)
	}
}
