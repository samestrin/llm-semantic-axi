package commands

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// jsTSCallSource is the same program in every JavaScript and TypeScript
// extension: an import, a direct call, and a this. method call inside a class.
// The class itself calls nothing outside its methods.
const jsTSCallSource = `import { readFile } from "./util/io";

function helper() {
    return 1;
}

function run() {
    return helper();
}

class Service {
    start() {
        return this.stop();
    }

    stop() {
        return helper();
    }
}
`

// storedRefRows reads back every stored edge of one file as
// "chunk ref_type ref_name", sorted.
func storedRefRows(t *testing.T, chunks []semantic.Chunk) []string {
	t.Helper()
	storage, err := semantic.NewSQLiteStorage(filepath.Join(".index", "semantic.db"), 0)
	if err != nil {
		t.Fatalf("reopen index: %v", err)
	}
	defer storage.Close()
	var rows []string
	for _, c := range chunks {
		refs, err := storage.GetRefs(context.Background(), c.ID)
		if err != nil {
			t.Fatalf("GetRefs(%s): %v", c.Name, err)
		}
		for _, r := range refs {
			rows = append(rows, c.Name+" "+string(r.RefType)+" "+r.RefName)
		}
	}
	sort.Strings(rows)
	return rows
}

// TestRunIndexStoresJSAndTSCallEdges is epic 03.1 T1's "done when", read from
// the database. It indexes through runIndex, so the chunker is the registered
// factory one, wrapped in the parse-error fallback: before the fallback
// forwarded RefExtractor, and before the tree-sitter JavaScript and TypeScript
// chunkers had one, every file here stored zero edges, and callers and refs
// answered empty for them. It also pins two things a wrong edge would come
// from: a member access is not a type use, so no uses_type row; and a call
// belongs to the innermost chunk, so the class does not "call" what its
// methods call.
func TestRunIndexStoresJSAndTSCallEdges(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	files := []string{"sample.ts", "sample.tsx", "sample.js", "sample.jsx"}
	for _, name := range files {
		writeFile(t, filepath.Join(dir, name), jsTSCallSource)
	}

	runIndexIn(t, ".", indexOpts{skipCalibration: true})
	byFile := storedChunks(t)

	// The import attaches to the file's first chunk, as Go's does.
	want := []string{
		"helper imports io",
		"run calls helper",
		"start calls this.stop",
		"stop calls helper",
	}
	for _, name := range files {
		got := storedRefRows(t, byFile[name])
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s edges:\n  got  %q\n  want %q", name, got, want)
		}
	}
}
