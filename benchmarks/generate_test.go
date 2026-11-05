package benchmarks

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Defends reproducibility of the outputs: the stand-in embedder must give the
// same vector for the same text on every run and every machine, and a unit
// vector so cosine scores stay in range.
func TestHashEmbedderIsDeterministicAndNormalised(t *testing.T) {
	h := HashEmbedder{Dims: 64}
	a := h.Vector("Where is the retry logic?")
	b := h.Vector("where is the RETRY logic")
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same words, different case or punctuation, gave different vectors")
	}
	if len(a) != 64 {
		t.Fatalf("len = %d, want 64", len(a))
	}
	var norm float64
	for _, x := range a {
		norm += x * x
	}
	if math.Abs(norm-1) > 1e-9 {
		t.Fatalf("norm^2 = %v, want 1", norm)
	}
	if reflect.DeepEqual(a, h.Vector("sqlite schema")) {
		t.Fatal("different text gave the same vector")
	}
	for _, x := range h.Vector("") {
		if x != 0 {
			t.Fatal("empty text gave a non-zero vector")
		}
	}
}

// Defends the wire shape the CLI's OpenAI embedder parses: a batch request
// answered out of shape fails indexing.
func TestHashEmbedderServesTheOpenAIShape(t *testing.T) {
	h := HashEmbedder{Dims: 8}
	srv := httptest.NewServer(h)
	defer srv.Close()

	for _, input := range []any{"one", []string{"one", "two"}} {
		body, _ := json.Marshal(map[string]any{"input": input, "model": "m"})
		resp, err := http.Post(srv.URL+"/v1/embeddings", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Data []struct {
				Index     int       `json:"index"`
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		want := 1
		if s, ok := input.([]string); ok {
			want = len(s)
		}
		if len(got.Data) != want {
			t.Fatalf("input %v: %d embeddings, want %d", input, len(got.Data), want)
		}
		for i, d := range got.Data {
			if d.Index != i || !reflect.DeepEqual(d.Embedding, h.Vector([]string{"one", "two"}[i])) {
				t.Fatalf("input %v: item %d = %+v", input, i, d)
			}
		}
	}

	resp, err := http.Post(srv.URL+"/v1/embeddings", "application/json", strings.NewReader("{not json"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed request: status %d, want 400", resp.StatusCode)
	}
	resp, err = http.Post(srv.URL+"/v1/embeddings", "application/json", strings.NewReader(`{"input":[1]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-string input: status %d, want 400", resp.StatusCode)
	}
}

// fakeCLI writes a shell script standing in for llm-semantic. It prints a
// result naming a file under $FAKE_ROOT in the requested format, or what
// $FAKE_JSON says for --json, and exits with $FAKE_EXIT.
func fakeCLI(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "llm-semantic")
	script := `#!/bin/sh
case " $* " in
  *" --json "*) printf '%s\n' "${FAKE_JSON:-[{\"chunk\":{\"file_path\":\"$FAKE_ROOT/a.go\"}}]}" ;;
  *" --axi "*) printf 'results[1]{file}:\n  %s/a.go:1\ntotal: 1\n' "$FAKE_ROOT" ;;
  *) printf '1 results for "%s"\n\n1. %s/a.go:1\n' "$2" "$FAKE_ROOT" ;;
esac
exit "${FAKE_EXIT:-0}"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func runConfig(t *testing.T, root string) RunConfig {
	return RunConfig{
		CLI:     fakeCLI(t),
		Dir:     root,
		APIURL:  "http://127.0.0.1:1",
		Model:   "m",
		Queries: []string{"first", "second"},
		OutDir:  t.TempDir(),
		Roots:   []string{root},
		RootAs:  "/work/repo",
	}
}

// Defends the checked-in outputs against leaking the machine they were made
// on: the temporary checkout path is rewritten in every format.
func TestRunQueriesWritesEveryFormatWithTheRootRewritten(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FAKE_ROOT", root)
	cfg := runConfig(t, root)
	if err := RunQueries(context.Background(), cfg); err != nil {
		t.Fatalf("RunQueries: %v", err)
	}
	for _, f := range Formats {
		for i := range cfg.Queries {
			data, err := os.ReadFile(OutputPath(cfg.OutDir, f, i))
			if err != nil {
				t.Fatalf("%s q%d: %v", f.Dir, i+1, err)
			}
			if strings.Contains(string(data), root) || !strings.Contains(string(data), "/work/repo/a.go") {
				t.Errorf("%s q%d root not rewritten: %q", f.Dir, i+1, data)
			}
		}
	}
}

// Defends against benchmarking a search that found nothing: an empty result
// array is tiny in every format and would make the numbers meaningless.
func TestRunQueriesRefusesAnEmptyResult(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FAKE_ROOT", root)
	t.Setenv("FAKE_JSON", "[]")
	if err := RunQueries(context.Background(), runConfig(t, root)); err == nil || !strings.Contains(err.Error(), "no results") {
		t.Fatalf("RunQueries with an empty result: err = %v, want no results", err)
	}
}

func TestRunQueriesRefusesUnparseableJSON(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FAKE_ROOT", root)
	t.Setenv("FAKE_JSON", "not json")
	if err := RunQueries(context.Background(), runConfig(t, root)); err == nil {
		t.Fatal("RunQueries with unparseable JSON returned no error")
	}
}

// Defends against checking in the output of a failed command as if it were a
// result.
func TestRunQueriesRefusesANonZeroExit(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FAKE_ROOT", root)
	t.Setenv("FAKE_EXIT", "1")
	if err := RunQueries(context.Background(), runConfig(t, root)); err == nil {
		t.Fatal("RunQueries with a failing CLI returned no error")
	}
}

// Defends against an output still carrying the temporary path in a form the
// rewrite did not catch (for example a different spelling of the root).
func TestRunQueriesRefusesALeakedRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FAKE_ROOT", root)
	cfg := runConfig(t, root)
	cfg.Roots = []string{filepath.Join(root, "elsewhere")}
	cfg.Leak = filepath.Dir(root)
	if err := RunQueries(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "temporary path") {
		t.Fatalf("RunQueries with a leaked root: err = %v, want temporary path", err)
	}
}

func TestReadQueriesSkipsBlankAndCommentLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), QueriesFile)
	if err := os.WriteFile(path, []byte("# a comment\n\nfirst query\n  second query  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadQueries(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"first query", "second query"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadQueries = %q, want %q", got, want)
	}
}
