package commands

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// TestReadmeEndpointClaimNamesQdrant defends the How it works sentence on
// callers and refs: it said they "never call the endpoint", which is false
// on Qdrant, where opening the collection probes the endpoint for the
// embedding dimension (probeEmbeddingDim). A sentence that says they never
// call it must also say what Qdrant does.
func TestReadmeEndpointClaimNamesQdrant(t *testing.T) {
	readme := readPublicDoc(t, "README.md")
	sentence := regexp.MustCompile("[^.]*`callers` and `refs` read the stored edges[^.]*\\.")
	found := sentence.FindAllString(readme, -1)
	if len(found) == 0 {
		t.Fatal("README.md no longer says how callers and refs read the edges; update this test with it")
	}
	for _, s := range found {
		if strings.Contains(s, "never call the endpoint") && !strings.Contains(s, "Qdrant") {
			t.Errorf("README.md says callers and refs never call the endpoint, which is false on Qdrant:\n%s", s)
		}
	}
}

// TestCallersAndRefsEndpointUseByBackend proves the README sentence both ways:
// over SQLite callers and refs send nothing to the embedding endpoint, and on
// Qdrant the dimension probe they run before opening storage sends one request.
func TestCallersAndRefsEndpointUseByBackend(t *testing.T) {
	mixedWorkspace(t)
	fe := newFakeEmbedder(t)
	useEmbedder(t, fe)

	captureStdout(t, func() {
		if err := runCallers(context.Background(), "validate_token", false, false); err != nil {
			t.Fatalf("runCallers: %v", err)
		}
		if err := runRefs(context.Background(), "validate_token", false, false); err != nil {
			t.Fatalf("runRefs: %v", err)
		}
	})
	fe.mu.Lock()
	sqliteCalls := fe.calls
	fe.mu.Unlock()
	if sqliteCalls != 0 {
		t.Errorf("callers and refs over SQLite sent %d requests to the endpoint, want 0", sqliteCalls)
	}

	t.Cleanup(SetStorageTypeForTesting("qdrant"))
	embedder, err := createEmbedder()
	if err != nil {
		t.Fatalf("createEmbedder: %v", err)
	}
	if _, err := probeEmbeddingDim(context.Background(), embedder); err != nil {
		t.Fatalf("probeEmbeddingDim: %v", err)
	}
	fe.mu.Lock()
	defer fe.mu.Unlock()
	if fe.calls != 1 {
		t.Errorf("the Qdrant dimension probe sent %d requests, want 1", fe.calls)
	}
}
