package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// indexedWorkspace builds a one-file index the search commands can query.
func indexedWorkspace(t *testing.T) string {
	t.Helper()
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)
	captureStdout(t, func() {
		if err := runIndex(context.Background(), ".", indexOpts{skipCalibration: true}); err != nil {
			t.Fatalf("runIndex: %v", err)
		}
	})
	return dir
}

// ===== runSearch =====

// TestSearchReturnsTheMatchingSymbol pins the default human output, including
// the file:line a caller needs in order to go and read the code.
func TestSearchReturnsTheMatchingSymbol(t *testing.T) {
	indexedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate bearer token identity", searchOpts{topK: 5}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})

	if !strings.Contains(out, "Authenticate") {
		t.Errorf("the matching symbol is missing:\n%s", out)
	}
	if !strings.Contains(out, "sample.go:") {
		t.Errorf("the result carries no file:line:\n%s", out)
	}
	if !strings.Contains(out, "Score:") {
		t.Errorf("the result carries no score:\n%s", out)
	}
}

// TestSearchJSONCarriesTheChunk pins the machine shape of a result.
func TestSearchJSONCarriesTheChunk(t *testing.T) {
	indexedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "render invoice billing", searchOpts{
			topK: 5, jsonOutput: true,
		}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})

	var results []struct {
		Score float64 `json:"score"`
		Chunk struct {
			Name     string `json:"name"`
			FilePath string `json:"file_path"`
		} `json:"chunk"`
	}
	decodeJSON(t, out, &results)
	if len(results) == 0 {
		t.Fatalf("--json returned no results:\n%s", out)
	}
	if results[0].Chunk.Name == "" || results[0].Chunk.FilePath == "" {
		t.Errorf("result chunk = %+v, want a named chunk with a path", results[0].Chunk)
	}
	if results[0].Score <= 0 {
		t.Errorf("score = %v, want a positive similarity", results[0].Score)
	}
}

// TestSearchMinimalKeepsOnlyTheLocator: --min is what an agent reads, and its
// whole value is being smaller than --json.
func TestSearchMinimalKeepsOnlyTheLocator(t *testing.T) {
	indexedWorkspace(t)

	full := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate token", searchOpts{
			topK: 5, jsonOutput: true,
		}); err != nil {
			t.Fatalf("runSearch --json: %v", err)
		}
	})
	minimal := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate token", searchOpts{
			topK: 5, minOutput: true,
		}); err != nil {
			t.Fatalf("runSearch --min: %v", err)
		}
	})

	var minResults []map[string]any
	decodeJSON(t, minimal, &minResults)
	if len(minResults) == 0 {
		t.Fatalf("--min returned no results:\n%s", minimal)
	}
	for _, key := range []string{"file", "name", "line", "score"} {
		if _, present := minResults[0][key]; !present {
			t.Errorf("--min dropped %q: %v", key, minResults[0])
		}
	}
	if _, present := minResults[0]["chunk"]; present {
		t.Errorf("--min carried the whole chunk: %v", minResults[0])
	}
	if len(minimal) >= len(full) {
		t.Errorf("--min output (%d bytes) is not smaller than --json (%d bytes)", len(minimal), len(full))
	}
}

// TestSearchReportsNoResultsRatherThanNothing: silence reads as a crash.
func TestSearchReportsNoResultsRatherThanNothing(t *testing.T) {
	indexedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate", searchOpts{
			topK: 5, threshold: 0.99,
		}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})

	if !strings.Contains(out, "No results found.") {
		t.Errorf("an empty result set printed %q", out)
	}
}

// TestSearchHybridAndPrefilterBothReturnResults covers the two alternative
// retrieval paths, which bypass the plain Search call entirely.
func TestSearchHybridAndPrefilterBothReturnResults(t *testing.T) {
	indexedWorkspace(t)

	for _, tt := range []struct {
		name string
		opts searchOpts
	}{
		{"hybrid", searchOpts{topK: 5, hybrid: true, fusionK: 60, fusionAlpha: 0.7, jsonOutput: true}},
		{"prefilter", searchOpts{topK: 5, prefilter: true, prefilterTopK: 20, jsonOutput: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() {
				if err := runSearch(context.Background(), "authenticate bearer token", tt.opts); err != nil {
					t.Fatalf("runSearch: %v", err)
				}
			})
			var results []map[string]any
			decodeJSON(t, out, &results)
			if len(results) == 0 {
				t.Errorf("%s search returned nothing:\n%s", tt.name, out)
			}
		})
	}
}

// TestSearchFiltersByPath proves --path narrows the result set instead of being
// accepted and ignored.
func TestSearchFiltersByPath(t *testing.T) {
	indexedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "authenticate token", searchOpts{
			topK: 5, pathFilter: "no/such/path", jsonOutput: true,
		}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})

	var results []map[string]any
	decodeJSON(t, out, &results)
	if len(results) != 0 {
		t.Errorf("--path no/such/path still returned %d results", len(results))
	}
}

// TestSearchWithoutAnIndexNamesTheFix keeps the missing-index diagnosis the
// same across every command that reads the index.
func TestSearchWithoutAnIndexNamesTheFix(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))

	err := runSearch(context.Background(), "anything", searchOpts{topK: 5})
	if err == nil {
		t.Fatal("searching with no index returned nil")
	}
	if !strings.Contains(err.Error(), "llm-semantic index") {
		t.Errorf("error = %v, want it to name the indexing command", err)
	}
}

// TestSearchRejectsRerankWithNoRerankerConfigured: silently searching without
// reranking would return worse results than the caller asked for, with no
// indication why.
func TestSearchRejectsRerankWithNoRerankerConfigured(t *testing.T) {
	indexedWorkspace(t)
	t.Setenv("LLM_SEMANTIC_RERANKER_API_URL", "")

	err := runSearch(context.Background(), "authenticate", searchOpts{topK: 5, rerank: true})
	if err == nil {
		t.Fatal("--rerank was accepted with no reranker configured")
	}
	if !strings.Contains(err.Error(), "LLM_SEMANTIC_RERANKER_API_URL") {
		t.Errorf("error = %v, want it to name the variable that enables reranking", err)
	}
}

// TestSearchSurfacesAnEmbedderFailure: the query itself has to be embedded, so
// a dead embedder is fatal here even though it is survivable while indexing.
func TestSearchSurfacesAnEmbedderFailure(t *testing.T) {
	indexedWorkspace(t)
	t.Cleanup(SetAPIURLForTesting(newBrokenEmbedder(t).URL))

	err := runSearch(context.Background(), "authenticate", searchOpts{topK: 5})
	if err == nil {
		t.Fatal("searching against a failing embedder returned nil")
	}
	if !strings.Contains(err.Error(), "search failed") {
		t.Errorf("error = %v, want it wrapped as a search failure", err)
	}
}

// ===== outputJSON / outputText =====

// sampleSearchResult is a hand-built result so the formatters can be checked
// against fields a real index would not reliably produce, such as Relevance.
func sampleSearchResult() semantic.SearchResult {
	return semantic.SearchResult{
		Chunk: semantic.Chunk{
			FilePath:  "pkg/auth.go",
			Name:      "Authenticate",
			Type:      semantic.ChunkFunction,
			StartLine: 42,
			Signature: "func Authenticate(token string) error",
		},
		Score:     0.8125,
		Relevance: "high",
	}
}

// TestOutputTextRendersRelevanceAndSignature pins the human result block.
func TestOutputTextRendersRelevanceAndSignature(t *testing.T) {
	out := captureStdout(t, func() {
		if err := outputText([]semantic.SearchResult{sampleSearchResult()}); err != nil {
			t.Fatalf("outputText: %v", err)
		}
	})

	if !strings.Contains(out, "1. pkg/auth.go:42 - Authenticate (function) [HIGH]") {
		t.Errorf("header line is wrong:\n%s", out)
	}
	if !strings.Contains(out, "Score: 0.8125") {
		t.Errorf("score formatting changed:\n%s", out)
	}
	if !strings.Contains(out, "func Authenticate(token string) error") {
		t.Errorf("the signature fallback did not render:\n%s", out)
	}
}

// TestOutputTextPrefersThePreviewOverTheSignature: when both exist the preview
// is the more useful line, and printing both doubles the output.
func TestOutputTextPrefersThePreviewOverTheSignature(t *testing.T) {
	r := sampleSearchResult()
	r.Preview = "validates a bearer token"

	out := captureStdout(t, func() {
		if err := outputText([]semantic.SearchResult{r}); err != nil {
			t.Fatalf("outputText: %v", err)
		}
	})

	if !strings.Contains(out, "validates a bearer token") {
		t.Errorf("the preview is missing:\n%s", out)
	}
	if strings.Contains(out, "func Authenticate(token string) error") {
		t.Errorf("both the preview and the signature were printed:\n%s", out)
	}
}

// TestOutputJSONMinimalCarriesRelevanceAndPreviewOnlyWhenSet keeps the minimal
// payload minimal: empty optional fields must be omitted, not sent as "".
func TestOutputJSONMinimalCarriesRelevanceAndPreviewOnlyWhenSet(t *testing.T) {
	withPreview := sampleSearchResult()
	withPreview.Preview = "validates a bearer token"
	bare := sampleSearchResult()
	bare.Relevance = ""

	rich := captureStdout(t, func() {
		if err := outputJSON([]semantic.SearchResult{withPreview}, true); err != nil {
			t.Fatalf("outputJSON: %v", err)
		}
	})
	plain := captureStdout(t, func() {
		if err := outputJSON([]semantic.SearchResult{bare}, true); err != nil {
			t.Fatalf("outputJSON: %v", err)
		}
	})

	var richResults, plainResults []map[string]any
	decodeJSON(t, rich, &richResults)
	decodeJSON(t, plain, &plainResults)
	if richResults[0]["r"] != "high" || richResults[0]["pr"] != "validates a bearer token" {
		t.Errorf("minimal result = %v, want relevance and preview", richResults[0])
	}
	if _, present := plainResults[0]["r"]; present {
		t.Errorf("an empty relevance was emitted: %v", plainResults[0])
	}
	if _, present := plainResults[0]["pr"]; present {
		t.Errorf("an empty preview was emitted: %v", plainResults[0])
	}
}

// ===== findIndexPath =====

// TestFindIndexPathPrefersTheConfiguredDirectory pins priority 1 of the lookup.
func TestFindIndexPathPrefersTheConfiguredDirectory(t *testing.T) {
	dir := newWorkspace(t)
	writeFile(t, filepath.Join(dir, "custom", "semantic.db"), "")
	writeFile(t, filepath.Join(dir, ".index", "semantic.db"), "")
	indexDir = "custom"

	got := findIndexPath()
	if want := filepath.Join("custom", "semantic.db"); got != want {
		t.Errorf("findIndexPath = %q, want %q", got, want)
	}
}

// TestFindIndexPathFallsBackToDotIndex covers priority 2, which is what every
// default invocation actually uses.
func TestFindIndexPathFallsBackToDotIndex(t *testing.T) {
	dir := newWorkspace(t)
	writeFile(t, filepath.Join(dir, ".index", "semantic.db"), "")
	indexDir = "nowhere"

	got := findIndexPath()
	if want := filepath.Join(".index", "semantic.db"); got != want {
		t.Errorf("findIndexPath = %q, want %q", got, want)
	}
}

// TestFindIndexPathReturnsEmptyWhenThereIsNoIndex, which is how every caller
// detects the missing-index case.
func TestFindIndexPathReturnsEmptyWhenThereIsNoIndex(t *testing.T) {
	newWorkspace(t)
	indexDir = "nowhere"

	if got := findIndexPath(); got != "" {
		t.Errorf("findIndexPath = %q, want an empty string", got)
	}
}

// ===== findGitRoot =====

// TestFindGitRootUsesTheWorkingDirectory, unlike findGitRootFrom which takes a
// starting path. Confusing the two would anchor the index in the wrong repo.
func TestFindGitRootUsesTheWorkingDirectory(t *testing.T) {
	dir := newWorkspace(t)
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")
	sub := filepath.Join(dir, "a", "b")
	writeFile(t, filepath.Join(sub, "placeholder"), "")
	t.Chdir(sub)

	got, err := findGitRoot()
	if err != nil {
		t.Fatalf("findGitRoot: %v", err)
	}
	// The temp root is a symlink on macOS, so compare the final component
	// rather than the literal path.
	if filepath.Base(got) != filepath.Base(dir) {
		t.Errorf("findGitRoot = %q, want the repository at %q", got, dir)
	}
}

// ===== initSearchComponents =====

// TestInitSearchComponentsWrapsAStorageFailure so the caller learns the index
// could not be opened rather than getting a nil storage.
func TestInitSearchComponentsWrapsAStorageFailure(t *testing.T) {
	indexedWorkspace(t)

	_, _, err := initSearchComponents(context.Background(),
		func(string, int) (semantic.Storage, error) {
			return nil, errBoom
		})
	if err == nil {
		t.Fatal("a failing storage constructor returned nil")
	}
	if !strings.Contains(err.Error(), "failed to open index") {
		t.Errorf("error = %v, want it wrapped as an index-open failure", err)
	}
}

// TestInitSearchComponentsCleansUpTheStorage: leaking the handle keeps the
// SQLite file locked for the rest of the process.
func TestInitSearchComponentsCleansUpTheStorage(t *testing.T) {
	indexedWorkspace(t)

	components, cleanup, err := initSearchComponents(context.Background(), createStorage)
	if err != nil {
		t.Fatalf("initSearchComponents: %v", err)
	}
	if components.Storage == nil || components.Embedder == nil {
		t.Fatalf("components = %+v, want both a storage and an embedder", components)
	}
	cleanup()

	if _, err := components.Storage.Stats(context.Background()); err == nil {
		t.Error("the storage is still usable after cleanup; it was never closed")
	}
}

// TestInitSearchComponentsProbesTheEmbedderForQdrant: SQLite never needs the
// dimension, Qdrant always does, and skipping the probe for Qdrant creates a
// zero-dimension collection.
func TestInitSearchComponentsProbesTheEmbedderForQdrant(t *testing.T) {
	newWorkspace(t)
	fe := newFakeEmbedder(t)
	useEmbedder(t, fe)
	t.Cleanup(SetStorageTypeForTesting("qdrant"))

	var gotDim int
	_, _, err := initSearchComponents(context.Background(),
		func(_ string, dim int) (semantic.Storage, error) {
			gotDim = dim
			return nil, errBoom
		})
	if err == nil {
		t.Fatal("expected the stub storage error to surface")
	}
	if gotDim != fakeEmbedderDim {
		t.Errorf("storage was built with dimension %d, want the probed %d", gotDim, fakeEmbedderDim)
	}
	if fe.callCount() == 0 {
		t.Error("qdrant storage was built without probing the embedder")
	}
}

// ===== searchAliasCmd =====

// TestSearchAliasPinsItsProfile: code-search and docs-search exist so a caller
// does not have to remember --profile, so the profile must actually be applied.
func TestSearchAliasPinsItsProfile(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))

	cmd := searchAliasCmd("docs-search", "docs")
	if cmd.Name() != "docs-search" {
		t.Errorf("alias name = %q, want docs-search", cmd.Name())
	}
	if cmd.Flags().Lookup("profiles") != nil {
		t.Error("an alias must not expose --profiles; the point is that it is fixed")
	}

	// With no index the command still has to fail the same way search does,
	// which proves the alias routes into the same handler.
	cmd.SetArgs([]string{"anything"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "semantic index not found") {
		t.Errorf("alias error = %v, want the same missing-index error search gives", err)
	}
}

// ===== createReranker =====

// TestCreateRerankerIsNilWhenUnconfigured. Reranking is optional, so an absent
// URL must not be an error.
func TestCreateRerankerIsNilWhenUnconfigured(t *testing.T) {
	newWorkspace(t)
	t.Setenv("LLM_SEMANTIC_RERANKER_API_URL", "")
	rerankerAPIURL = ""
	t.Cleanup(func() { rerankerAPIURL = "" })

	reranker, err := createReranker()
	if err != nil {
		t.Fatalf("createReranker: %v", err)
	}
	if reranker != nil {
		t.Errorf("createReranker = %v, want nil when nothing is configured", reranker)
	}
}

// TestCreateRerankerUsesTheEnvironmentDefaults covers the fallback chain: the
// URL comes from the environment and the model from its own default.
func TestCreateRerankerUsesTheEnvironmentDefaults(t *testing.T) {
	newWorkspace(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LLM_SEMANTIC_RERANKER_API_URL", srv.URL)
	t.Setenv("LLM_SEMANTIC_RERANKER_MODEL", "")
	rerankerAPIURL = ""
	rerankerModel = ""
	t.Cleanup(func() { rerankerAPIURL, rerankerModel = "", "" })

	reranker, err := createReranker()
	if err != nil {
		t.Fatalf("createReranker: %v", err)
	}
	if reranker == nil {
		t.Fatal("createReranker = nil despite a configured URL")
	}
	if got := getDefaultRerankerModel(); got != "Qwen/Qwen3-Reranker-0.6B" {
		t.Errorf("default reranker model = %q, want the documented default", got)
	}
}

// TestGetDefaultRerankerSettingsReadTheEnvironment pins both accessors, which
// are the only way the reranker is configured without a flag.
func TestGetDefaultRerankerSettingsReadTheEnvironment(t *testing.T) {
	t.Setenv("LLM_SEMANTIC_RERANKER_API_URL", "http://reranker.invalid")
	t.Setenv("LLM_SEMANTIC_RERANKER_MODEL", "custom/model")

	if got := getDefaultRerankerAPIURL(); got != "http://reranker.invalid" {
		t.Errorf("getDefaultRerankerAPIURL = %q, want the environment value", got)
	}
	if got := getDefaultRerankerModel(); got != "custom/model" {
		t.Errorf("getDefaultRerankerModel = %q, want the environment value", got)
	}
}
