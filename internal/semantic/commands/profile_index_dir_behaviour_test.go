package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/v2/internal/semantic"
)

const profileDocsSource = `# Deployment guide

Rotate the staging credentials before every release window.
`

// profileWorkspace is a repository with one code file and one docs file, and
// an embedder the commands talk to.
func profileWorkspace(t *testing.T) *fakeEmbedder {
	t.Helper()
	dir := newWorkspace(t)
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)
	// A .git directory anchors the default index at the workspace root.
	for _, sub := range []string{".git", "docs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	writeFile(t, filepath.Join(dir, "docs", "guide.md"), profileDocsSource)
	return newFakeEmbedder(t)
}

// runRoot executes the CLI as a user would, so the root hook that resolves
// the index directory runs, and returns what it printed.
func runRoot(t *testing.T, args ...string) string {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		root := RootCmd()
		root.SetArgs(args)
		err = root.Execute()
	})
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return out
}

// filesIndexedIn opens the sqlite index under dir and returns the files it holds.
func filesIndexedIn(t *testing.T, dir string) map[string]bool {
	t.Helper()
	path := filepath.Join(dir, "semantic.db")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no index at %s: %v", path, err)
	}
	storage, err := semantic.NewSQLiteStorage(path, 0)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer storage.Close()
	files, err := storage.ListIndexedFiles(context.Background())
	if err != nil {
		t.Fatalf("ListIndexedFiles: %v", err)
	}
	return files
}

func holds(files map[string]bool, suffix string) bool {
	for f := range files {
		if strings.HasSuffix(f, suffix) {
			return true
		}
	}
	return false
}

// searchPaths runs a search through the CLI and returns each result's file.
func searchPaths(t *testing.T, args ...string) []string {
	t.Helper()
	out := runRoot(t, append(args, "--json", "--top", "10")...)
	var results []struct {
		Chunk struct {
			FilePath string `json:"file_path"`
		} `json:"chunk"`
	}
	decodeJSON(t, out, &results)
	paths := make([]string, 0, len(results))
	for _, r := range results {
		paths = append(paths, r.Chunk.FilePath)
	}
	return paths
}

// TestNonCodeProfileGetsItsOwnIndex defends debt row c2185b99ed738d07: the
// docs profile shared .index with the code profile, so `index docs/ --profile
// docs` after `index .` skipped every docs file as "already indexed" and a docs
// search answered with code chunks. A non-code profile now defaults to
// .index/<profile>.
func TestNonCodeProfileGetsItsOwnIndex(t *testing.T) {
	fe := profileWorkspace(t)

	runRoot(t, "index", ".", "--skip-calibration", "--plain", "--api-url", fe.URL)
	out := runRoot(t, "index", "docs/", "--profile", "docs", "--skip-calibration", "--plain", "--api-url", fe.URL)

	if strings.Contains(out, "Skipped") {
		t.Errorf("the docs index skipped files the code index holds:\n%s", out)
	}
	docs := filesIndexedIn(t, filepath.Join(".index", "docs"))
	if !holds(docs, "guide.md") {
		t.Errorf(".index/docs does not hold the docs file: %v", docs)
	}
	if holds(docs, "sample.go") {
		t.Errorf(".index/docs holds a code file: %v", docs)
	}

	paths := searchPaths(t, "search", "rotate staging credentials release", "--profile", "docs", "--api-url", fe.URL)
	if len(paths) == 0 {
		t.Fatal("a docs search over the docs index found nothing")
	}
	for _, p := range paths {
		if strings.HasSuffix(p, ".go") {
			t.Errorf("a docs search returned a code chunk: %v", paths)
			break
		}
	}

	// The alias is "search --profile docs", so it reads the same index.
	aliasPaths := searchPaths(t, "docs-search", "rotate staging credentials release", "--api-url", fe.URL)
	if len(aliasPaths) == 0 {
		t.Fatal("docs-search found nothing in the docs index")
	}
	for _, p := range aliasPaths {
		if strings.HasSuffix(p, ".go") {
			t.Errorf("docs-search returned a code chunk: %v", aliasPaths)
			break
		}
	}
}

// TestNonCodeProfileSearchDoesNotFallBackToTheCodeIndex: before the docs
// profile has an index, a docs search must say so, not answer from the code
// index in .index, which is the same wrong answer by another route.
func TestNonCodeProfileSearchDoesNotFallBackToTheCodeIndex(t *testing.T) {
	fe := profileWorkspace(t)
	runRoot(t, "index", ".", "--skip-calibration", "--plain", "--api-url", fe.URL)

	root := RootCmd()
	root.SetArgs([]string{"search", "render invoice billing", "--profile", "docs", "--json", "--api-url", fe.URL})
	var err error
	out := captureStdout(t, func() { err = root.Execute() })

	if err == nil || !strings.Contains(err.Error(), "index not found") {
		t.Errorf("a docs search with no docs index did not report it missing: err=%v\n%s", err, out)
	}
}

// TestProfileIndexDirKeepsExplicitAndConfiguredPaths: the default applies
// only when nothing chose a path. An explicit --index-dir (even the default
// value), a loaded config, and the code profile keep .index, so no existing
// setup moves.
func TestProfileIndexDirKeepsExplicitAndConfiguredPaths(t *testing.T) {
	cases := map[string]struct {
		args   []string
		config bool
		want   string
	}{
		"explicit --index-dir .index": {args: []string{"--profile", "docs", "--index-dir", ".index"}, want: ".index"},
		"explicit --index-dir other":  {args: []string{"--profile", "docs", "--index-dir", "custom"}, want: "custom"},
		"auto-discovered config":      {args: []string{"--profile", "docs"}, config: true, want: ".index"},
		"code profile":                {args: []string{"--profile", "code"}, want: ".index"},
		"no profile":                  {args: nil, want: ".index"},
		"docs profile":                {args: []string{"--profile", "docs"}, want: filepath.Join(".index", "docs")},
		"memory profile":              {args: []string{"--profile", "memory"}, want: filepath.Join(".index", "memory")},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fe := profileWorkspace(t)
			if tc.config {
				writeFile(t, filepath.Join(".planning", ".config", "config.yaml"), "semantic:\n  code_collection: code\n")
			}
			args := append([]string{"index", "docs/", "--skip-calibration", "--plain", "--api-url", fe.URL}, tc.args...)
			runRoot(t, args...)

			if got := indexDir; got != tc.want {
				t.Errorf("indexDir = %q, want %q", got, tc.want)
			}
			if !holds(filesIndexedIn(t, tc.want), "guide.md") {
				t.Errorf("the docs file was not indexed into %s", tc.want)
			}
		})
	}
}

// TestProfileIndexDirNamesTheQdrantCollection: Qdrant follows the profile
// through the collection derived from the index directory, so a docs profile
// no longer writes into the shared default collection.
func TestProfileIndexDirNamesTheQdrantCollection(t *testing.T) {
	newWorkspace(t)
	t.Setenv("QDRANT_COLLECTION", "")
	// Only the root hook runs, so nothing reaches a Qdrant server.
	root := RootCmd()
	if err := root.ParseFlags([]string{"--profile", "docs", "--storage", "qdrant"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if err := root.PersistentPreRunE(root, nil); err != nil {
		t.Fatalf("root hook: %v", err)
	}
	if got := resolveCollectionName(); got != "docs" {
		t.Errorf("collection = %q, want docs", got)
	}
}
