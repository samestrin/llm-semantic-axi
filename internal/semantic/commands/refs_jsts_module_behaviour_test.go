package commands

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/v2/internal/semantic"
)

// jsModuleCallAppSource calls init (declared in the lib file) and bootstrap
// (declared here) at file scope, outside every function chunk. Before epic 40
// neither call recorded an edge, so `callers init` answered empty though this
// file calls it. The same-file call pins the recursion rule at command level:
// `<module> calls bootstrap` must be recorded, never `bootstrap calls bootstrap`.
const jsModuleCallAppSource = `const PORT = 8080;

function bootstrap() {
    return helper();
}

init(PORT);
bootstrap();
`

// jsModuleCallLibSource declares the callees. console.log is a member call,
// which records no edge, so this file's chunks store none.
const jsModuleCallLibSource = `function init(port) {
    console.log(port);
}

function helper() {
    return 1;
}
`

// jsModuleCallWorkspace indexes the fixture through runIndex into a real
// sqlite index in t.TempDir(), so the edges under test come from the whole
// pipeline — chunker, extractor, storage, resolution — not from a hand-seeded
// row as in T1's display test.
func jsModuleCallWorkspace(t *testing.T) map[string][]semantic.Chunk {
	t.Helper()
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "app.js"), jsModuleCallAppSource)
	writeFile(t, filepath.Join(dir, "lib.js"), jsModuleCallLibSource)
	runIndexIn(t, ".", indexOpts{skipCalibration: true})
	return storedChunks(t)
}

// TestRunIndexStoresModuleLevelCallEdges is epic 40 T3's "done when", read
// from the database: a JS call at file scope must store a calls edge owned by
// the <module> chunk. Before T2 the extractor dropped every call no function
// chunk held, so this file stored only the edge inside bootstrap.
func TestRunIndexStoresModuleLevelCallEdges(t *testing.T) {
	byFile := jsModuleCallWorkspace(t)

	want := []string{
		"<module> calls bootstrap",
		"<module> calls init",
		"bootstrap calls helper",
	}
	if got := storedRefRows(t, byFile["app.js"]); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("app.js edges:\n  got  %q\n  want %q", got, want)
	}
	if got := storedRefRows(t, byFile["lib.js"]); len(got) != 0 {
		t.Errorf("lib.js edges = %q, want none: a member call records no edge", got)
	}
}

// TestCallersNamesTheModuleChunkForAModuleLevelCall defends the answer
// `callers init` gives for a file-scope call: it must name <module> in the
// calling file. callers prints no chunk type, so the reserved name is the
// only thing saying the call is at module scope; an empty answer here was the
// bug epic 40 exists to fix.
func TestCallersNamesTheModuleChunkForAModuleLevelCall(t *testing.T) {
	jsModuleCallWorkspace(t)

	out := captureStdout(t, func() {
		if err := runCallers(context.Background(), "init", false, false, false); err != nil {
			t.Fatalf("runCallers: %v", err)
		}
	})
	for _, want := range []string{"1 caller(s) of init", "app.js", "<module>"} {
		if !strings.Contains(out, want) {
			t.Errorf("callers of init lacks %q:\n%s", want, out)
		}
	}
}

// TestCallersOfAFunctionCalledAtFileScopeShowsNoSelfEdge pins the recursion
// rule end to end: bootstrap is called at file scope in the file that declares
// it, and the only caller must be <module>. Attributing the call to the first
// chunk instead (Python's old rule) would record `bootstrap calls bootstrap`
// and report two callers, one of them a confident lie.
func TestCallersOfAFunctionCalledAtFileScopeShowsNoSelfEdge(t *testing.T) {
	jsModuleCallWorkspace(t)

	out := captureStdout(t, func() {
		if err := runCallers(context.Background(), "bootstrap", false, false, false); err != nil {
			t.Fatalf("runCallers: %v", err)
		}
	})
	for _, want := range []string{"1 caller(s) of bootstrap", "app.js", "<module>"} {
		if !strings.Contains(out, want) {
			t.Errorf("callers of bootstrap lacks %q:\n%s", want, out)
		}
	}
}

// TestRefsFromTheModuleChunkListsItsResolvedCalls defends the reverse
// direction: `refs <module>` must list the file-scope calls with the files
// they resolve to, not report them external or answer empty. lib.js also has
// a <module> chunk with no edges, so this also pins that an edgeless module
// chunk adds no rows.
func TestRefsFromTheModuleChunkListsItsResolvedCalls(t *testing.T) {
	jsModuleCallWorkspace(t)

	out := captureStdout(t, func() {
		if err := runRefs(context.Background(), "<module>", false, false, false); err != nil {
			t.Fatalf("runRefs: %v", err)
		}
	})
	for _, want := range []string{
		"2 reference(s) from <module>",
		"calls",
		"init",
		"lib.js:1",
		"bootstrap",
		"app.js:3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("refs from <module> lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "(external)") {
		t.Errorf("refs from <module> marks an indexed callee external:\n%s", out)
	}
}
