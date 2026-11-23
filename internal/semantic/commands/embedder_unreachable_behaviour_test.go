package commands

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/v2/pkg/output"
)

// EST-8171 item 1: with no embedding API running, `search`, `memory search`
// and `index` printed Go's transport error verbatim (`failed to send request:
// Post "http://localhost:8080/v1/embeddings": dial tcp ... connection
// refused`) with no help, so an agent learned nothing about what to change.
// The failure must name the endpoint in plain words and say how to fix it.

// deadEmbedderURL is the address of an embedding API that is not running: a
// server that has been started and closed, so the dial is refused in-process
// without touching the network.
func deadEmbedderURL(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.NotFoundHandler())
	url := s.URL
	s.Close()
	return url
}

// assertExplainsUnreachable checks the message is plain, names the URL, and
// carries the next step.
func assertExplainsUnreachable(t *testing.T, err error, url string) {
	t.Helper()
	if err == nil {
		t.Fatal("a dead embedder was reported as success")
	}
	msg := err.Error()
	for _, raw := range []string{"failed to send request", "dial tcp", "Post \""} {
		if strings.Contains(msg, raw) {
			t.Errorf("message leaks the transport error %q: %s", raw, msg)
		}
	}
	if !strings.Contains(msg, "cannot reach the embedding API at "+url) {
		t.Errorf("message does not name the unreachable endpoint %s: %s", url, msg)
	}
	var h output.AXIHelper
	if !errors.As(err, &h) {
		t.Fatalf("error carries no help: %v", err)
	}
	help := strings.Join(h.AXIHelp(), "\n")
	for _, want := range []string{"--api-url", "LLM_SEMANTIC_API_URL"} {
		if !strings.Contains(help, want) {
			t.Errorf("help does not mention %s:\n%s", want, help)
		}
	}
	if output.ExitCodeFor(err) != 1 {
		t.Errorf("exit code %d, want 1", output.ExitCodeFor(err))
	}
}

func TestSearchExplainsAnUnreachableEmbedder(t *testing.T) {
	indexedWorkspace(t)
	url := deadEmbedderURL(t)
	t.Cleanup(SetAPIURLForTesting(url))

	err := runSearch(context.Background(), "authenticate", searchOpts{topK: 5})

	assertExplainsUnreachable(t, err, url)
}

func TestMemorySearchExplainsAnUnreachableEmbedder(t *testing.T) {
	newWorkspace(t)
	url := deadEmbedderURL(t)
	t.Cleanup(SetAPIURLForTesting(url))
	t.Cleanup(SetEmbedderTypeForTesting("openai"))
	seedMemories(t, newMemory("mem-auth", "How are tokens validated?", "Verify the signature"))

	err := runMemorySearch(context.Background(), memorySearchOpts{query: "tokens", topK: 5})

	assertExplainsUnreachable(t, err, url)
}

// TestIndexExplainsAnUnreachableEmbedder: index reports its own failure
// document, so the help has to be in that document, and the per-file errors
// must not carry the raw transport error either.
func TestIndexExplainsAnUnreachableEmbedder(t *testing.T) {
	dir := newWorkspace(t)
	withAXI(t)
	url := deadEmbedderURL(t)
	t.Cleanup(SetAPIURLForTesting(url))
	t.Cleanup(SetEmbedderTypeForTesting("openai"))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)

	out, err := runIndexFailing(t, indexOpts{skipCalibration: true})

	if output.ExitCodeFor(err) != 1 {
		t.Errorf("exit code %d, want 1", output.ExitCodeFor(err))
	}
	for _, raw := range []string{"failed to send request", "dial tcp"} {
		if strings.Contains(out, raw) {
			t.Errorf("output leaks the transport error %q:\n%s", raw, out)
		}
	}
	for _, want := range []string{"error: true", "no file was indexed", "cannot reach the embedding API at " + url, "help[", "--api-url", "LLM_SEMANTIC_API_URL"} {
		if !strings.Contains(out, want) {
			t.Errorf("TOON output lacks %q:\n%s", want, out)
		}
	}
}

// TestIndexUpdateExplainsAnUnreachableEmbedder: the commit hook runs
// index-update, so it needs the same plain message and help as index.
func TestIndexUpdateExplainsAnUnreachableEmbedder(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)
	buildIndex(t)
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource+"\nfunc Added() {}\n")
	withAXI(t)
	url := deadEmbedderURL(t)
	t.Cleanup(SetAPIURLForTesting(url))

	var err error
	out := captureStdout(t, func() {
		err = runIndexUpdate(context.Background(), ".", updateOpts{noGit: true})
	})

	if output.ExitCodeFor(err) != 1 {
		t.Errorf("exit code %d, want 1", output.ExitCodeFor(err))
	}
	for _, want := range []string{"error: true", "cannot reach the embedding API at " + url, "help[", "--api-url"} {
		if !strings.Contains(out, want) {
			t.Errorf("TOON output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "dial tcp") {
		t.Errorf("output leaks the transport error:\n%s", out)
	}
}

// TestIndexJSONKeepsItsSchemaWhenTheEmbedderIsUnreachable: help is a TOON
// field, as it is for every other failure; --json keeps its pre-AXI keys.
func TestIndexJSONKeepsItsSchemaWhenTheEmbedderIsUnreachable(t *testing.T) {
	dir := newWorkspace(t)
	withPlain(t)
	t.Cleanup(SetAPIURLForTesting(deadEmbedderURL(t)))
	t.Cleanup(SetEmbedderTypeForTesting("openai"))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)

	out, _ := runIndexFailing(t, indexOpts{skipCalibration: true, jsonOutput: true})

	var payload map[string]any
	decodeJSON(t, out, &payload)
	if _, ok := payload["help"]; ok {
		t.Errorf("--json gained a help key:\n%s", out)
	}
	if msg, _ := payload["message"].(string); !strings.Contains(msg, "cannot reach the embedding API") {
		t.Errorf("--json message does not explain the failure: %q", msg)
	}
}
