package commands

import (
	"path/filepath"
	"strings"
	"testing"
)

// runRootErr executes the CLI as a user would and returns its error, for the
// cases that are meant to fail.
func runRootErr(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		root := RootCmd()
		root.SetArgs(args)
		err = root.Execute()
	})
	return out, err
}

// TestCollectionDeleteProfileDeletesFromTheProfileIndex defends EST-9003:
// `collection delete --profile docs` has its own --profile flag, so the root
// hook never gave it the docs index. It opened .index while the docs chunks
// live in .index/docs, printed "No chunks found for domain 'docs'", exited 0
// and deleted nothing.
func TestCollectionDeleteProfileDeletesFromTheProfileIndex(t *testing.T) {
	fe := profileWorkspace(t)
	runRoot(t, "index", ".", "--skip-calibration", "--plain", "--api-url", fe.URL)
	runRoot(t, "index", "docs/", "--profile", "docs", "--skip-calibration", "--plain", "--api-url", fe.URL)
	if !holds(filesIndexedIn(t, filepath.Join(".index", "docs")), "guide.md") {
		t.Fatal("the docs index holds no docs file, so the delete would pass vacuously")
	}
	codeBefore := filesIndexedIn(t, ".index")

	out := runRoot(t, "collection", "delete", "--profile", "docs", "--force", "--plain", "--api-url", fe.URL)

	if strings.Contains(out, "No chunks found") {
		t.Errorf("the delete reported an empty docs domain:\n%s", out)
	}
	if docs := filesIndexedIn(t, filepath.Join(".index", "docs")); holds(docs, "guide.md") {
		t.Errorf(".index/docs still holds the docs file after the delete: %v", docs)
	}
	codeAfter := filesIndexedIn(t, ".index")
	if len(codeAfter) != len(codeBefore) || !holds(codeAfter, "sample.go") {
		t.Errorf("deleting the docs profile changed the code index: %v -> %v", codeBefore, codeAfter)
	}
}

// TestCollectionDeleteProfileWithoutItsIndexSaysSo: with no docs index the
// delete must not open the code index and report an empty docs domain, which
// reads as "deleted, there was nothing" when nothing was looked at.
func TestCollectionDeleteProfileWithoutItsIndexSaysSo(t *testing.T) {
	fe := profileWorkspace(t)
	runRoot(t, "index", ".", "--skip-calibration", "--plain", "--api-url", fe.URL)
	codeBefore := filesIndexedIn(t, ".index")

	out, err := runRootErr(t, "collection", "delete", "--profile", "docs", "--force", "--plain", "--api-url", fe.URL)

	if err == nil {
		t.Fatalf("deleting a profile with no index exited 0:\n%s", out)
	}
	if !strings.Contains(err.Error(), "docs") || !strings.Contains(err.Error(), filepath.Join(".index", "docs")) {
		t.Errorf("error = %v, want it to name the docs profile and its index", err)
	}
	if strings.Contains(out, "No chunks found") {
		t.Errorf("a missing profile index was reported as an empty delete:\n%s", out)
	}
	if got := filesIndexedIn(t, ".index"); len(got) != len(codeBefore) {
		t.Errorf("the code index changed: %v -> %v", codeBefore, got)
	}
}

// TestCollectionDeleteProfileKeepsExplicitAndConfiguredPaths: the profile
// default applies only when nothing chose a path, as for index and search. An
// explicit --index-dir and a loaded config keep theirs, so a delete still
// reaches an index built at that path.
func TestCollectionDeleteProfileKeepsExplicitAndConfiguredPaths(t *testing.T) {
	cases := map[string]struct {
		args   []string
		config bool
		want   string
	}{
		"explicit --index-dir .index": {args: []string{"--index-dir", ".index"}, want: ".index"},
		"explicit --index-dir other":  {args: []string{"--index-dir", "custom"}, want: "custom"},
		"auto-discovered config":      {config: true, want: ".index"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fe := profileWorkspace(t)
			if tc.config {
				writeFile(t, filepath.Join(".planning", ".config", "config.yaml"), "semantic:\n  code_collection: code\n")
			}
			runRoot(t, append([]string{"index", "docs/", "--profile", "docs", "--skip-calibration", "--plain", "--api-url", fe.URL}, tc.args...)...)
			if !holds(filesIndexedIn(t, tc.want), "guide.md") {
				t.Fatalf("the docs file was not indexed into %s", tc.want)
			}

			runRoot(t, append([]string{"collection", "delete", "--profile", "docs", "--force", "--plain", "--api-url", fe.URL}, tc.args...)...)

			if indexDir != tc.want {
				t.Errorf("indexDir = %q, want %q", indexDir, tc.want)
			}
			if holds(filesIndexedIn(t, tc.want), "guide.md") {
				t.Errorf("%s still holds the docs file after the delete", tc.want)
			}
		})
	}
}

// TestCollectionDeleteProfileNamesTheQdrantCollection: with Qdrant the docs
// profile's chunks live in the collection derived from .index/docs, so the
// delete must address "docs", not the shared default "llm_semantic". No
// QDRANT_API_URL is set, so opening the storage fails before any network.
func TestCollectionDeleteProfileNamesTheQdrantCollection(t *testing.T) {
	fe := profileWorkspace(t)
	t.Setenv("QDRANT_COLLECTION", "")
	t.Setenv("QDRANT_API_URL", "")

	_, err := runRootErr(t, "collection", "delete", "--profile", "docs", "--force", "--storage", "qdrant", "--plain", "--api-url", fe.URL)

	if err == nil || !strings.Contains(err.Error(), "failed to open storage") {
		t.Fatalf("err = %v, want the storage open to fail without a Qdrant URL", err)
	}
	if got := resolveCollectionName(); got != "docs" {
		t.Errorf("collection = %q, want docs", got)
	}
}

// TestCollectionDeleteProfileUsesTheProfileStorageFromConfig defends the
// Reviewer's finding on EST-9003: the root hook resolved the storage backend
// for the code profile, because the delete's --profile shadows the root one.
// With `docs_storage: qdrant` the delete opened sqlite, printed "No chunks
// found for domain 'docs'" and exited 0 while the docs chunks sat in Qdrant.
// No QDRANT_API_URL is set, so reaching Qdrant fails before any network.
func TestCollectionDeleteProfileUsesTheProfileStorageFromConfig(t *testing.T) {
	fe := profileWorkspace(t)
	t.Setenv("QDRANT_API_URL", "")
	runRoot(t, "index", ".", "--skip-calibration", "--plain", "--api-url", fe.URL)
	writeFile(t, filepath.Join(".planning", ".config", "config.yaml"), "semantic:\n  code_storage: sqlite\n  docs_storage: qdrant\n")

	out, err := runRootErr(t, "collection", "delete", "--profile", "docs", "--force", "--plain", "--api-url", fe.URL)

	if err == nil || !strings.Contains(err.Error(), "failed to open storage") {
		t.Fatalf("err = %v, want the docs profile's Qdrant storage to be opened\n%s", err, out)
	}
	if strings.Contains(out, "No chunks found") {
		t.Errorf("the delete reported an empty domain from the wrong backend:\n%s", out)
	}
	if storageType != "qdrant" {
		t.Errorf("storageType = %q, want qdrant", storageType)
	}
}
