package semantic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestReranker_Rerank_RestoresOriginalOrder pins the mapping back from the API's
// score-sorted response to the caller's document order: scores[i] must belong to
// documents[i], and an out-of-range index from a misbehaving server is ignored rather
// than panicking.
func TestReranker_Rerank_RestoresOriginalOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{"index": 2, "relevance_score": 0.9},
				{"index": 0, "relevance_score": 0.2},
				{"index": 99, "relevance_score": 0.5}, // out of range, must be dropped
			},
		})
	}))
	defer server.Close()

	reranker, err := NewReranker(RerankerConfig{APIURL: server.URL, Model: "test-reranker"})
	if err != nil {
		t.Fatalf("NewReranker() error = %v", err)
	}

	scores, err := reranker.Rerank(context.Background(), "q", []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Rerank() error = %v", err)
	}
	if len(scores) != 3 {
		t.Fatalf("Rerank() returned %d scores, want one per document", len(scores))
	}
	if scores[0] != 0.2 {
		t.Errorf("scores[0] = %v, want 0.2", scores[0])
	}
	if scores[1] != 0 {
		t.Errorf("scores[1] = %v, want 0 (the API scored no result at index 1)", scores[1])
	}
	if scores[2] != 0.9 {
		t.Errorf("scores[2] = %v, want 0.9", scores[2])
	}
	if reranker.Model() != "test-reranker" {
		t.Errorf("Model() = %q, want test-reranker", reranker.Model())
	}
}

// TestReranker_Rerank_ErrorStatusIncludesBody pins that a non-200 reranker response is
// reported with both its status and its body, which is what makes a misconfigured
// endpoint diagnosable.
func TestReranker_Rerank_ErrorStatusIncludesBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"detail":"unknown model"}`))
	}))
	defer server.Close()

	reranker, err := NewReranker(RerankerConfig{APIURL: server.URL})
	if err != nil {
		t.Fatalf("NewReranker() error = %v", err)
	}

	_, err = reranker.Rerank(context.Background(), "q", []string{"a"})
	if err == nil {
		t.Fatal("Rerank() should fail on a 400 response")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "unknown model") {
		t.Errorf("error = %v, want it to carry the status and the response body", err)
	}
}

// TestReranker_Rerank_UnparseableBody pins that a non-JSON 200 response is reported as a
// parse failure rather than silently yielding all-zero scores.
func TestReranker_Rerank_UnparseableBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	reranker, err := NewReranker(RerankerConfig{APIURL: server.URL})
	if err != nil {
		t.Fatalf("NewReranker() error = %v", err)
	}

	_, err = reranker.Rerank(context.Background(), "q", []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "failed to parse response") {
		t.Errorf("error = %v, want a parse failure", err)
	}
}

// TestReranker_Rerank_SendsQueryAndDocuments pins the request body the reranker sends,
// so a change to the wire format cannot pass unnoticed.
func TestReranker_Rerank_SendsQueryAndDocuments(t *testing.T) {
	var got struct {
		Query     string   `json:"query"`
		Documents []string `json:"documents"`
		Model     string   `json:"model"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{{"index": 0, "relevance_score": 1.0}},
		})
	}))
	defer server.Close()

	reranker, err := NewReranker(RerankerConfig{APIURL: server.URL, Model: "bge-reranker"})
	if err != nil {
		t.Fatalf("NewReranker() error = %v", err)
	}

	if _, err := reranker.Rerank(context.Background(), "how to spin", []string{"doc one"}); err != nil {
		t.Fatalf("Rerank() error = %v", err)
	}
	if got.Query != "how to spin" {
		t.Errorf("request query = %q, want \"how to spin\"", got.Query)
	}
	if len(got.Documents) != 1 || got.Documents[0] != "doc one" {
		t.Errorf("request documents = %v, want [doc one]", got.Documents)
	}
	if got.Model != "bge-reranker" {
		t.Errorf("request model = %q, want bge-reranker", got.Model)
	}
}
