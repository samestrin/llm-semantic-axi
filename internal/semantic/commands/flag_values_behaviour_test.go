package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/v2/pkg/output"
)

// EST-8278 items 5, 8, 9 and 10: a bad flag value is a usage error. It must
// exit 2, as the skill promises, and fail before anything is created or sent:
// `index . --embedder bogus` created .index first and exited 1, `collection
// delete --profile bogus --force` exited 0, and `--top -1` or `--batch-size
// -1` ran with the value.
func TestBadFlagValuesExitTwoBeforeAnyWork(t *testing.T) {
	cases := [][]string{
		{"index", ".", "--embedder", "bogus"},
		{"search", "x", "--embedder", "bogus"},
		{"index", ".", "--profile", "bogus"},
		{"search", "x", "--profile", "bogus"},
		{"search", "x", "--profile", ""},
		{"search", "x", "--top", "-1"},
		{"search", "x", "--top", "0"},
		{"search", "x", "--threshold", "5"},
		{"search", "x", "--threshold", "-0.5"},
		{"search", "x", "--profiles", "bogus"},
		{"search", "x", "--profiles", "code,"},
		{"multisearch", "x", "y", "--top", "-1"},
		{"multisearch", "x", "y", "--threshold", "5"},
		{"multisearch", "x", "y", "--profiles", "bogus"},
		{"multisearch", "x", "y", "--output", "bogus"},
		{"memory", "search", "x", "--top", "-1"},
		{"memory", "stats", "--prune", "--older-than", "0", "--yes"},
		{"memory", "store", "--question", "", "--answer", "a"},
		{"memory", "store", "--question", "q", "--answer", "  "},
		{"index", ".", "--batch-size", "-1"},
		{"index", ".", "--parallel", "-1"},
		{"index", ".", "--embed-batch-size", "-1"},
		{"collection", "delete", "--profile", "bogus", "--force"},
		{"collection", "delete", "--profile", "a;rm -rf x", "--force"},
		// EST-8336 Low 1: these exited 1 with --hybrid or --recency-boost and
		// were silently accepted without them.
		{"search", "x", "--hybrid", "--fusion-k", "-1"},
		{"search", "x", "--fusion-k", "0"},
		{"search", "x", "--hybrid", "--fusion-alpha", "7"},
		{"search", "x", "--fusion-alpha", "-0.1"},
		{"search", "x", "--recency-boost", "--recency-decay", "-1"},
		{"search", "x", "--recency-decay", "0"},
		{"search", "x", "--recency-factor", "-1"},
		{"code-search", "x", "--hybrid", "--fusion-k", "-1"},
		{"docs-search", "x", "--fusion-alpha", "7"},
		{"code-search", "x", "--recency-decay", "-1"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			newWorkspace(t)
			os.WriteFile("main.go", []byte("package main\n\nfunc main() {}\n"), 0o644)
			fe := newFakeEmbedder(t)
			useEmbedder(t, fe)

			err := executeRoot(t, append(args, "--api-url", fe.URL)...)

			if err == nil {
				t.Fatal("accepted")
			}
			if code := output.ExitCodeFor(err); code != 2 {
				t.Errorf("exit code %d, want 2: %v", code, err)
			}
			if n := fe.callCount(); n != 0 {
				t.Errorf("embedder received %d calls before the value was rejected", n)
			}
			if _, statErr := os.Stat(".index"); !os.IsNotExist(statErr) {
				t.Errorf(".index was created before the value was rejected")
			}
		})
	}
}

// TestBadFlagValueNamesTheFlagAndTheValidValues: the rejection says which
// flag, what was given, and what it takes.
func TestBadFlagValueNamesTheFlagAndTheValidValues(t *testing.T) {
	newWorkspace(t)
	err := executeRoot(t, "index", ".", "--embedder", "bogus")
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{`"bogus"`, "--embedder", "openai", "cohere"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q: %v", want, err)
		}
	}
}

// TestCollectionDeleteHelpLineIsSafeToPaste (item 9): the refusal prints a
// command to rerun; a profile name carrying shell syntax must never reach it.
func TestCollectionDeleteHelpLineIsSafeToPaste(t *testing.T) {
	newWorkspace(t)
	err := executeRoot(t, "collection", "delete", "--profile", "a;rm -rf x")
	if err == nil {
		t.Fatal("accepted")
	}
	for _, h := range axiHelpOf(err) {
		if strings.Contains(h, "rm -rf") {
			t.Errorf("help line carries the unvalidated profile: %s", h)
		}
	}
}

// TestIndexDirTildeIsExpanded (item 10): `--index-dir '~/idx'` (quoted, so
// the shell did not expand it) created a directory literally named ~.
func TestIndexDirTildeIsExpanded(t *testing.T) {
	dir := newWorkspace(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.WriteFile("main.go", []byte("package main\n\nfunc main() {}\n"), 0o644)
	fe := newFakeEmbedder(t)
	useEmbedder(t, fe)

	if err := executeRoot(t, "index", ".", "--index-dir", "~/idx", "--skip-calibration", "--api-url", fe.URL); err != nil {
		t.Fatalf("index: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "~")); !os.IsNotExist(err) {
		t.Errorf("a directory literally named ~ was created")
	}
	if _, err := os.Stat(filepath.Join(home, "idx", "semantic.db")); err != nil {
		t.Errorf("index not written under $HOME/idx: %v", err)
	}
}
