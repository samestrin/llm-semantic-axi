package commands

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/v2/internal/semantic"
)

// moduleChunkWorkspace indexes a JS plain script, which holds no function, and
// a TS file declaring the function the script calls. It returns the stored
// chunks by file name.
func moduleChunkWorkspace(t *testing.T) map[string][]semantic.Chunk {
	t.Helper()
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "script.js"), "const PORT = 8080;\ninit(PORT);\n")
	writeFile(t, filepath.Join(dir, "lib.ts"), "export function init(port: number): void {\n    console.log(port);\n}\n")
	runIndexIn(t, ".", indexOpts{skipCalibration: true})
	return storedChunks(t)
}

// TestRunIndexStoresAModuleChunkForAPlainJSScript is epic 40 T1's "done when",
// read from the database: a JS file of top-level statements stored no chunk
// at all, so its code could not be searched and its module-level calls had
// no caller to be recorded against.
func TestRunIndexStoresAModuleChunkForAPlainJSScript(t *testing.T) {
	byFile := moduleChunkWorkspace(t)

	if got := storedRows(byFile["script.js"]); strings.Join(got, ",") != "<module> file 1-2" {
		t.Errorf("script.js rows = %v, want [<module> file 1-2]", got)
	}
	if got := storedRows(byFile["lib.ts"]); strings.Join(got, ",") != "<module> file 1-3,init function 1-3" {
		t.Errorf("lib.ts rows = %v, want [<module> file 1-3 init function 1-3]", got)
	}
}

// TestSearchPrintsTheModuleChunkByName defends a module-scope hit being
// recognisable: the reserved name is what tells a reader the match is the
// file's top level, not a function.
func TestSearchPrintsTheModuleChunkByName(t *testing.T) {
	moduleChunkWorkspace(t)

	out := captureStdout(t, func() {
		if err := runSearch(context.Background(), "init PORT", searchOpts{topK: 10}); err != nil {
			t.Fatalf("runSearch: %v", err)
		}
	})
	if !strings.Contains(out, "<module>") || !strings.Contains(out, "script.js") {
		t.Errorf("search does not show script.js's <module> chunk:\n%s", out)
	}
}

// TestCallersPrintsTheModuleChunkAsTheCaller defends the name callers shows
// for a module-level call: callers prints no chunk type, so `<module>` is the
// only thing saying the call is at file scope. T2 writes these edges; here one
// is stored against the indexed chunk directly, so the display is pinned now.
func TestCallersPrintsTheModuleChunkAsTheCaller(t *testing.T) {
	byFile := moduleChunkWorkspace(t)
	chunks := byFile["script.js"]
	if len(chunks) != 1 || chunks[0].Name != "<module>" {
		t.Fatalf("script.js chunks = %v, want only <module>", storedRows(chunks))
	}

	storage, err := semantic.NewSQLiteStorage(filepath.Join(".index", "semantic.db"), 0)
	if err != nil {
		t.Fatalf("reopen index: %v", err)
	}
	ctx := context.Background()
	if err := storage.StoreRefs(ctx, []semantic.ChunkRef{{ChunkID: chunks[0].ID, RefType: semantic.RefCalls, RefName: "init"}}); err != nil {
		t.Fatalf("StoreRefs: %v", err)
	}
	if err := storage.ResolveRefs(ctx); err != nil {
		t.Fatalf("ResolveRefs: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("close index: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runCallers(ctx, "init", false, false, false); err != nil {
			t.Fatalf("runCallers: %v", err)
		}
	})
	if !strings.Contains(out, "<module>") || !strings.Contains(out, "script.js") {
		t.Errorf("callers does not name <module> in script.js:\n%s", out)
	}
}
