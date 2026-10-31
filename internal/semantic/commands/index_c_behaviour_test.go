package commands

import (
	"path/filepath"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

const cIndexSource = `#include <stdio.h>

struct Point {
    int x;
};

enum Color { RED, GREEN };

typedef struct {
    int id;
} Record;

static char *dup_string(const char *s) {
    return 0;
}
`

// TestRunIndexStoresCDefinitionsWithNamesAndLines is T5's "done when": a .c
// file indexed end to end must leave rows with the right names and line
// ranges. Before the C chunker `.c` had no chunker, so the file was skipped and
// the index held no row for it at all.
func TestRunIndexStoresCDefinitionsWithNamesAndLines(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.c"), cIndexSource)

	runIndexIn(t, ".", indexOpts{skipCalibration: true})

	got := map[string][2]int{}
	for _, c := range storedChunks(t)["sample.c"] {
		got[c.Name] = [2]int{c.StartLine, c.EndLine}
	}
	want := map[string][2]int{"Point": {3, 5}, "Color": {7, 7}, "Record": {9, 11}, "dup_string": {13, 15}}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for name, lines := range want {
		if got[name] != lines {
			t.Errorf("%s lines = %v, want %v", name, got[name], lines)
		}
	}
	for _, c := range storedChunks(t)["sample.c"] {
		if c.Type == semantic.ChunkFile {
			t.Errorf("%s stored as text window; C file should parse cleanly", c.Name)
		}
	}
}
