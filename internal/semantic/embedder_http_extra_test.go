package semantic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestEmbedder_EmbedBatch_RequestAndOrdering pins the OpenAI-compatible batch request:
// a multi-text call sends an array input plus the model, the bearer token is attached,
// and responses are placed by their reported index rather than arrival order.
func TestEmbedder_EmbedBatch_RequestAndOrdering(t *testing.T) {
	var gotAuth, gotContentType string
	var gotInput []string
	var gotModel string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		json.Unmarshal(body, &req)
		gotInput = req.Input
		gotModel = req.Model

		// Deliberately out of order: index 1 first.
		json.NewEncoder(w).Encode(map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{"object": "embedding", "index": 1, "embedding": []float64{0.9, 0.9}},
				{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.1}},
			},
			"model": "test-model",
		})
	}))
	defer server.Close()

	embedder, err := NewEmbedder(EmbedderConfig{APIURL: server.URL, Model: "test-model", APIKey: "secret"})
	if err != nil {
		t.Fatalf("NewEmbedder() error = %v", err)
	}

	out, err := embedder.EmbedBatch(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("EmbedBatch() error = %v", err)
	}

	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization header = %q, want \"Bearer secret\"", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotModel != "test-model" {
		t.Errorf("request model = %q, want test-model", gotModel)
	}
	if len(gotInput) != 2 || gotInput[0] != "first" || gotInput[1] != "second" {
		t.Errorf("request input = %v, want both texts as an array", gotInput)
	}

	if len(out) != 2 {
		t.Fatalf("EmbedBatch() returned %d embeddings, want 2", len(out))
	}
	if out[0][0] != 0.1 {
		t.Errorf("out[0] = %v, want the index-0 embedding regardless of response order", out[0])
	}
	if out[1][0] != 0.9 {
		t.Errorf("out[1] = %v, want the index-1 embedding", out[1])
	}
	if embedder.Dimensions() != 2 {
		t.Errorf("Dimensions() = %d, want 2 (learned from the response)", embedder.Dimensions())
	}
	if embedder.Model() != "test-model" {
		t.Errorf("Model() = %q, want test-model", embedder.Model())
	}
}

// TestEmbedder_EmbedBatch_Empty pins the short-circuit: no texts means no HTTP call.
func TestEmbedder_EmbedBatch_Empty(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	embedder, err := NewEmbedder(EmbedderConfig{APIURL: server.URL})
	if err != nil {
		t.Fatalf("NewEmbedder() error = %v", err)
	}

	out, err := embedder.EmbedBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("EmbedBatch(nil) error = %v", err)
	}
	if out != nil {
		t.Errorf("EmbedBatch(nil) = %v, want nil", out)
	}
	if called {
		t.Error("EmbedBatch(nil) made an HTTP request; it must short-circuit")
	}
}

// TestEmbedder_EmbedBatch_APIErrorMessage pins that a structured API error is surfaced by
// its message, and a bodyless failure falls back to naming the status code.
func TestEmbedder_EmbedBatch_APIErrorMessage(t *testing.T) {
	structured := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"message": "model not found", "type": "invalid_request"},
		})
	}))
	defer structured.Close()

	embedder, err := NewEmbedder(EmbedderConfig{APIURL: structured.URL})
	if err != nil {
		t.Fatalf("NewEmbedder() error = %v", err)
	}
	_, err = embedder.EmbedBatch(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Errorf("error = %v, want the API's error message", err)
	}

	bare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer bare.Close()

	embedder2, err := NewEmbedder(EmbedderConfig{APIURL: bare.URL})
	if err != nil {
		t.Fatalf("NewEmbedder() error = %v", err)
	}
	_, err = embedder2.EmbedBatch(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "418") {
		t.Errorf("error = %v, want the status code", err)
	}
}

// TestEmbedder_EmbedBatch_RetriesOn5xx pins the retry loop: a 5xx is retried up to
// MaxRetries+1 attempts, and the last response is what gets reported.
func TestEmbedder_EmbedBatch_RetriesOn5xx(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	embedder, err := NewEmbedder(EmbedderConfig{APIURL: server.URL, MaxRetries: 2, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewEmbedder() error = %v", err)
	}

	_, err = embedder.EmbedBatch(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("EmbedBatch() should fail after exhausting retries")
	}
	if attempts != 3 {
		t.Errorf("server saw %d attempts, want 3 (MaxRetries=2 plus the first try)", attempts)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error = %v, want the final status code", err)
	}
}

// TestEmbedder_Embed_NoEmbeddingReturned pins the guard for a well-formed response that
// carries no data rows.
func TestEmbedder_Embed_NoEmbeddingReturned(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"object": "list", "data": []interface{}{}})
	}))
	defer server.Close()

	embedder, err := NewEmbedder(EmbedderConfig{APIURL: server.URL})
	if err != nil {
		t.Fatalf("NewEmbedder() error = %v", err)
	}

	_, err = embedder.Embed(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "no embedding returned") {
		t.Errorf("Embed() error = %v, want \"no embedding returned\"", err)
	}
}

// TestEmbedder_EmbedBatch_UnparseableBody pins that a non-JSON 200 response is reported
// as a parse failure rather than yielding an empty embedding list.
func TestEmbedder_EmbedBatch_UnparseableBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>not json</html>"))
	}))
	defer server.Close()

	embedder, err := NewEmbedder(EmbedderConfig{APIURL: server.URL})
	if err != nil {
		t.Fatalf("NewEmbedder() error = %v", err)
	}

	_, err = embedder.EmbedBatch(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "failed to parse response") {
		t.Errorf("error = %v, want a parse failure", err)
	}
}

// TestOpenRouterEmbedder_EmbedBatch pins the OpenRouter request shape (model, array
// input, float encoding, bearer auth) and that responses are ordered by index.
func TestOpenRouterEmbedder_EmbedBatch_RequestShape(t *testing.T) {
	var gotPath, gotAuth, gotEncoding string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		var req struct {
			EncodingFormat string `json:"encoding_format"`
		}
		json.Unmarshal(body, &req)
		gotEncoding = req.EncodingFormat

		json.NewEncoder(w).Encode(map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{"object": "embedding", "index": 1, "embedding": []float64{0.4, 0.5}},
				{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2}},
			},
			"model": "openai/text-embedding-3-small",
		})
	}))
	defer server.Close()

	embedder, err := NewOpenRouterEmbedder(OpenRouterConfig{
		APIKey:  "or-key",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewOpenRouterEmbedder() error = %v", err)
	}

	out, err := embedder.EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("EmbedBatch() error = %v", err)
	}
	if !strings.HasSuffix(gotPath, "/embeddings") {
		t.Errorf("request path = %q, want it to end with /embeddings", gotPath)
	}
	if gotAuth != "Bearer or-key" {
		t.Errorf("Authorization = %q, want \"Bearer or-key\"", gotAuth)
	}
	if gotEncoding != "float" {
		t.Errorf("encoding_format = %q, want \"float\"", gotEncoding)
	}
	if len(out) != 2 || out[0][0] != 0.1 || out[1][0] != 0.4 {
		t.Errorf("embeddings = %v, want them reordered by index", out)
	}
}

// TestOpenRouterEmbedder_APIError pins that an API error response is surfaced rather
// than returning an empty embedding set.
func TestOpenRouterEmbedder_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{"message": "invalid api key", "code": 401},
		})
	}))
	defer server.Close()

	embedder, err := NewOpenRouterEmbedder(OpenRouterConfig{APIKey: "bad", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenRouterEmbedder() error = %v", err)
	}

	_, err = embedder.EmbedBatch(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("error = %v, want the API error message", err)
	}
}

// TestCohereEmbedder_SetInputTypeAndModel pins the two small accessors: Model reports the
// configured model and SetInputType changes what subsequent requests send.
func TestCohereEmbedder_SetInputTypeAndModel(t *testing.T) {
	var gotInputType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			InputType string `json:"input_type"`
		}
		json.Unmarshal(body, &req)
		gotInputType = req.InputType
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":         "1",
			"embeddings": [][]float64{{0.1, 0.2}},
		})
	}))
	defer server.Close()

	embedder, err := NewCohereEmbedder(CohereConfig{
		APIKey:  "co-key",
		Model:   "embed-english-v3.0",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewCohereEmbedder() error = %v", err)
	}
	if embedder.Model() != "embed-english-v3.0" {
		t.Errorf("Model() = %q, want embed-english-v3.0", embedder.Model())
	}

	embedder.SetInputType("search_query")
	if _, err := embedder.EmbedBatch(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("EmbedBatch() error = %v", err)
	}
	if gotInputType != "search_query" {
		t.Errorf("request input_type = %q, want search_query after SetInputType", gotInputType)
	}
}

// TestEmbeddersFromEnv pins that each provider's FromEnv constructor reads its documented
// variables and that a missing API key is rejected by the shared config validation.
func TestEmbeddersFromEnv(t *testing.T) {
	t.Run("cohere", func(t *testing.T) {
		t.Setenv("COHERE_API_KEY", "co-key")
		t.Setenv("COHERE_MODEL", "embed-multilingual-v3.0")
		t.Setenv("COHERE_INPUT_TYPE", "search_query")

		embedder, err := NewCohereEmbedderFromEnv()
		if err != nil {
			t.Fatalf("NewCohereEmbedderFromEnv() error = %v", err)
		}
		if embedder.Model() != "embed-multilingual-v3.0" {
			t.Errorf("Model() = %q, want the COHERE_MODEL value", embedder.Model())
		}
	})

	t.Run("cohere missing key", func(t *testing.T) {
		t.Setenv("COHERE_API_KEY", "")
		if _, err := NewCohereEmbedderFromEnv(); err == nil {
			t.Error("NewCohereEmbedderFromEnv() with no key should fail validation")
		}
	})

	t.Run("openrouter", func(t *testing.T) {
		t.Setenv("OPENROUTER_API_KEY", "or-key")
		t.Setenv("OPENROUTER_MODEL", "openai/text-embedding-3-large")

		embedder, err := NewOpenRouterEmbedderFromEnv()
		if err != nil {
			t.Fatalf("NewOpenRouterEmbedderFromEnv() error = %v", err)
		}
		if embedder.Model() != "openai/text-embedding-3-large" {
			t.Errorf("Model() = %q, want the OPENROUTER_MODEL value", embedder.Model())
		}
	})

	t.Run("openrouter missing key", func(t *testing.T) {
		t.Setenv("OPENROUTER_API_KEY", "")
		if _, err := NewOpenRouterEmbedderFromEnv(); err == nil {
			t.Error("NewOpenRouterEmbedderFromEnv() with no key should fail validation")
		}
	})

	t.Run("huggingface falls back through key names", func(t *testing.T) {
		t.Setenv("HUGGING_FACE_API_KEY", "")
		t.Setenv("HUGGINGFACE_API_KEY", "")
		t.Setenv("HF_TOKEN", "hf-token")
		t.Setenv("HF_MODEL", "BAAI/bge-small-en-v1.5")
		t.Setenv("HF_WAIT_FOR_MODEL", "true")

		embedder, err := NewHuggingFaceEmbedderFromEnv()
		if err != nil {
			t.Fatalf("NewHuggingFaceEmbedderFromEnv() error = %v", err)
		}
		if embedder.Model() != "BAAI/bge-small-en-v1.5" {
			t.Errorf("Model() = %q, want the HF_MODEL value", embedder.Model())
		}
	})

	t.Run("huggingface missing key", func(t *testing.T) {
		t.Setenv("HUGGING_FACE_API_KEY", "")
		t.Setenv("HUGGINGFACE_API_KEY", "")
		t.Setenv("HF_TOKEN", "")
		if _, err := NewHuggingFaceEmbedderFromEnv(); err == nil {
			t.Error("NewHuggingFaceEmbedderFromEnv() with no key should fail validation")
		}
	})
}

// TestHuggingFaceEmbedder_ParseEmbeddings pins the four response shapes the HuggingFace
// feature-extraction endpoint can return, and the failure when it returns none of them.
func TestHuggingFaceEmbedder_ParseEmbeddings(t *testing.T) {
	newEmbedder := func(t *testing.T) *HuggingFaceEmbedder {
		t.Helper()
		e, err := NewHuggingFaceEmbedder(HuggingFaceConfig{APIKey: "k"})
		if err != nil {
			t.Fatalf("NewHuggingFaceEmbedder() error = %v", err)
		}
		return e
	}

	t.Run("1D single embedding", func(t *testing.T) {
		e := newEmbedder(t)
		out, err := e.parseEmbeddings([]byte(`[0.1,0.2,0.3]`), 1)
		if err != nil {
			t.Fatalf("parseEmbeddings() error = %v", err)
		}
		if len(out) != 1 || len(out[0]) != 3 || out[0][2] != 0.3 {
			t.Fatalf("parseEmbeddings() = %v, want one 3-dim embedding", out)
		}
		if e.Dimensions() != 3 {
			t.Errorf("Dimensions() = %d, want 3 (learned from the response)", e.Dimensions())
		}
	})

	t.Run("2D batch response", func(t *testing.T) {
		e := newEmbedder(t)
		out, err := e.parseEmbeddings([]byte(`[[0.1,0.2],[0.3,0.4]]`), 2)
		if err != nil {
			t.Fatalf("parseEmbeddings() error = %v", err)
		}
		if len(out) != 2 || out[1][1] != 0.4 {
			t.Fatalf("parseEmbeddings() = %v, want two embeddings", out)
		}
		if e.Dimensions() != 2 {
			t.Errorf("Dimensions() = %d, want 2", e.Dimensions())
		}
	})

	t.Run("3D per-token response is mean pooled", func(t *testing.T) {
		e := newEmbedder(t)
		// One input, two tokens: means are (0.1+0.3)/2 and (0.2+0.4)/2.
		out, err := e.parseEmbeddings([]byte(`[[[0.1,0.2],[0.3,0.4]]]`), 1)
		if err != nil {
			t.Fatalf("parseEmbeddings() error = %v", err)
		}
		if len(out) != 1 || len(out[0]) != 2 {
			t.Fatalf("parseEmbeddings() = %v, want one 2-dim pooled embedding", out)
		}
		if out[0][0] < 0.199 || out[0][0] > 0.201 {
			t.Errorf("pooled[0] = %v, want ~0.2", out[0][0])
		}
		if out[0][1] < 0.299 || out[0][1] > 0.301 {
			t.Errorf("pooled[1] = %v, want ~0.3", out[0][1])
		}
	})

	t.Run("unrecognised shape", func(t *testing.T) {
		e := newEmbedder(t)
		if _, err := e.parseEmbeddings([]byte(`{"error":"model loading"}`), 1); err == nil {
			t.Error("parseEmbeddings() on an object response should fail")
		}
	})
}

// TestHuggingFaceEmbedder_MeanPool pins the pooling arithmetic and its empty-input guard.
func TestHuggingFaceEmbedder_MeanPool(t *testing.T) {
	e, err := NewHuggingFaceEmbedder(HuggingFaceConfig{APIKey: "k"})
	if err != nil {
		t.Fatalf("NewHuggingFaceEmbedder() error = %v", err)
	}

	got := e.meanPool([][]float64{{1, 3}, {3, 5}})
	if len(got) != 2 || got[0] != 2 || got[1] != 4 {
		t.Errorf("meanPool() = %v, want [2 4]", got)
	}
	if e.meanPool(nil) != nil {
		t.Error("meanPool(nil) should return nil")
	}
}
