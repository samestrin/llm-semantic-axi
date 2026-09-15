package commands

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// The cobra runXxx handlers were all untested because every one of them opens
// an embedder and a storage backend before it does anything observable. Both
// dependencies are reachable without a service: SQLite writes to a temp dir,
// and the embedder speaks the OpenAI /v1/embeddings shape over HTTP, which an
// httptest server answers in-process. Nothing here touches the network — a
// test that reached for a real endpoint would pass on the one machine that had
// one running and fail everywhere else.

// errBoom stands in for any dependency failure a stub needs to raise.
var errBoom = errors.New("boom")

// fakeEmbedderDim is deliberately small. The vectors only have to be
// *comparable*, and a 32-slot bag of words makes a query about "authenticate"
// score higher against a chunk that mentions it than against one that does not,
// which is what the search and calibration paths actually depend on.
const fakeEmbedderDim = 32

// fakeEmbedder answers /v1/embeddings with deterministic bag-of-words vectors.
//
// Determinism matters: an embedder that returned random vectors would make
// every ranking assertion below flaky, and a flaky ranking assertion gets
// deleted rather than fixed.
type fakeEmbedder struct {
	*httptest.Server

	mu     sync.Mutex
	calls  int
	texts  []string
	status int // when non-zero, every request is answered with this status
}

// newFakeEmbedder starts an in-process embedding API and shuts it down with the
// test.
func newFakeEmbedder(t *testing.T) *fakeEmbedder {
	t.Helper()
	fe := &fakeEmbedder{}
	fe.Server = httptest.NewServer(http.HandlerFunc(fe.handle))
	t.Cleanup(fe.Server.Close)
	return fe
}

// newBrokenEmbedder starts an embedding API that rejects every request, so the
// error paths can be driven without waiting on a connection timeout. 400 rather
// than 500 because the embedder retries 5xx three times with backoff.
func newBrokenEmbedder(t *testing.T) *fakeEmbedder {
	t.Helper()
	fe := newFakeEmbedder(t)
	fe.status = http.StatusBadRequest
	return fe
}

func (fe *fakeEmbedder) handle(w http.ResponseWriter, r *http.Request) {
	if fe.status != 0 {
		w.WriteHeader(fe.status)
		_, _ = io.WriteString(w, `{"error":{"message":"embedder unavailable"}}`)
		return
	}

	var req struct {
		Input any    `json:"input"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var texts []string
	switch in := req.Input.(type) {
	case string:
		texts = []string{in}
	case []any:
		for _, v := range in {
			s, _ := v.(string)
			texts = append(texts, s)
		}
	}

	fe.mu.Lock()
	fe.calls++
	fe.texts = append(fe.texts, texts...)
	fe.mu.Unlock()

	type datum struct {
		Object    string    `json:"object"`
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	}
	resp := struct {
		Object string  `json:"object"`
		Data   []datum `json:"data"`
		Model  string  `json:"model"`
	}{Object: "list", Model: req.Model}

	for i, text := range texts {
		resp.Data = append(resp.Data, datum{Object: "embedding", Index: i, Embedding: bagOfWords(text)})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// callCount reports how many embedding requests the handlers made. Guarded
// because the handler runs on the server's goroutine and the suite runs under
// -race.
func (fe *fakeEmbedder) callCount() int {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	return fe.calls
}

// embeddedTexts returns every text the handlers asked to embed, which is how a
// test proves an entry reached the embedder with the content it expected.
func (fe *fakeEmbedder) embeddedTexts() []string {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	return append([]string(nil), fe.texts...)
}

// bagOfWords maps text onto a unit vector by hashing each word into a slot.
// Shared words move two texts closer, which gives cosine similarity a real
// meaning without an embedding model.
func bagOfWords(text string) []float64 {
	vec := make([]float64, fakeEmbedderDim)
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(word))
		vec[h.Sum32()%fakeEmbedderDim]++
	}

	var norm float64
	for _, v := range vec {
		norm += v * v
	}
	if norm == 0 {
		// An empty text still needs a non-zero vector; a zero vector makes
		// every cosine score NaN and the failure looks like a ranking bug.
		vec[0] = 1
		return vec
	}
	norm = math.Sqrt(norm)
	for i := range vec {
		vec[i] /= norm
	}
	return vec
}

// newWorkspace moves the test into an empty directory and guarantees every
// package global the commands mutate is back to its default afterwards.
//
// Restoring is not optional: storageType, indexDir, profile and the three
// Global*Output flags are package-level, so one test leaking a value makes an
// unrelated test fail somewhere later in the run.
func newWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	ResetGlobalsForTesting()
	t.Cleanup(ResetGlobalsForTesting)
	return dir
}

// useEmbedder points the commands at an in-process embedding API.
func useEmbedder(t *testing.T, fe *fakeEmbedder) {
	t.Helper()
	t.Cleanup(SetAPIURLForTesting(fe.URL))
	t.Cleanup(SetEmbedderTypeForTesting("openai"))
}

// seedMemories opens the SQLite index directly and stores entries with
// pre-computed vectors, so a memory command can be driven over a populated
// index without the store path having to succeed first.
func seedMemories(t *testing.T, entries ...semantic.MemoryEntry) string {
	t.Helper()
	path := filepath.Join(".index", "semantic.db")
	if err := os.MkdirAll(".index", 0o755); err != nil {
		t.Fatalf("mkdir .index: %v", err)
	}
	storage, err := semantic.NewSQLiteStorage(path, 0)
	if err != nil {
		t.Fatalf("NewSQLiteStorage: %v", err)
	}
	defer storage.Close()

	for _, e := range entries {
		vec := make([]float32, fakeEmbedderDim)
		for i, v := range bagOfWords(e.EmbeddingText()) {
			vec[i] = float32(v)
		}
		if err := storage.StoreMemory(context.Background(), e, vec); err != nil {
			t.Fatalf("StoreMemory(%s): %v", e.ID, err)
		}
	}
	return path
}

// newMemory builds a pending entry with a caller-chosen ID, because the
// generated ID embeds today's date and a test that hard-codes one breaks at
// midnight.
func newMemory(id, question, answer string, tags ...string) semantic.MemoryEntry {
	e := semantic.NewMemoryEntry(question, answer)
	e.ID = id
	if len(tags) > 0 {
		e.Tags = tags
	}
	return *e
}

// writeFile creates a file and any parent directories it needs.
func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// sampleGoSource is indexed by several tests. It carries distinct vocabulary
// per function so the bag-of-words embedder can tell the chunks apart.
const sampleGoSource = `package sample

// Authenticate validates a bearer token and returns the caller identity.
func Authenticate(token string) (string, error) {
	return token, nil
}

// RenderInvoice formats billing lines into a printable invoice document.
func RenderInvoice(lines []string) string {
	return lines[0]
}
`

// captureStderr mirrors captureStdout for the handlers that report degraded
// operations as warnings rather than errors.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	defer func() {
		_ = w.Close()
		os.Stderr = orig
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	return <-done
}

// withStdin replaces os.Stdin with a file holding the given text, which is how
// the two fmt.Scanln confirmation prompts are driven. Without it those prompts
// read whatever the test runner left on stdin.
func withStdin(t *testing.T, input string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatalf("write stdin file: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open stdin file: %v", err)
	}
	orig := os.Stdin
	t.Cleanup(func() {
		os.Stdin = orig
		_ = f.Close()
	})
	os.Stdin = f
}

// decodeJSON parses captured stdout, failing the test with the raw text when it
// is not JSON — a handler that printed prose instead of JSON is the bug most of
// these tests exist to catch.
func decodeJSON(t *testing.T, out string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), v); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
}
