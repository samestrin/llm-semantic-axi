package commands

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// callGraphSource carries a call this package can resolve: Caller invokes
// Authenticate, and both are indexed, so exactly one symbol carries the name.
const callGraphSource = `package sample

func Authenticate(token string) error {
	return nil
}

func Caller(token string) error {
	return Authenticate(token)
}
`

// callGraphWorkspace indexes a file whose call edges the Go chunker records.
func callGraphWorkspace(t *testing.T) {
	t.Helper()
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), callGraphSource)
	captureStdout(t, func() {
		if err := runIndex(context.Background(), ".", indexOpts{skipCalibration: true}); err != nil {
			t.Fatalf("runIndex: %v", err)
		}
	})
}

// ===== runCallers / runRefs =====

// TestCallersFindsTheCallingFunction is the command's whole promise: the call
// graph recorded at index time answers "what calls this".
func TestCallersFindsTheCallingFunction(t *testing.T) {
	callGraphWorkspace(t)

	out := captureStdout(t, func() {
		if err := runCallers(context.Background(), "Authenticate", false, false); err != nil {
			t.Fatalf("runCallers: %v", err)
		}
	})

	if !strings.Contains(out, "Caller") {
		t.Errorf("the calling function is missing:\n%s", out)
	}
	if !strings.Contains(out, "caller(s) of Authenticate") {
		t.Errorf("the summary line is wrong:\n%s", out)
	}
}

// TestRefsListsWhatASymbolCalls is the reverse direction, and unlike callers it
// also reports references it could not resolve.
func TestRefsListsWhatASymbolCalls(t *testing.T) {
	callGraphWorkspace(t)

	out := captureStdout(t, func() {
		if err := runRefs(context.Background(), "Caller", false, false); err != nil {
			t.Fatalf("runRefs: %v", err)
		}
	})

	if !strings.Contains(out, "Authenticate") {
		t.Errorf("the called symbol is missing:\n%s", out)
	}
	if !strings.Contains(out, "reference(s) from Caller") {
		t.Errorf("the summary line is wrong:\n%s", out)
	}
}

// TestCallersOfAnUnindexedSymbolIsNotAnError: an empty answer is a legitimate
// result, and exiting non-zero would break any script that asks routinely.
func TestCallersOfAnUnindexedSymbolIsNotAnError(t *testing.T) {
	callGraphWorkspace(t)

	out := captureStdout(t, func() {
		if err := runCallers(context.Background(), "NoSuchSymbol", false, false); err != nil {
			t.Fatalf("runCallers must not error on an empty result: %v", err)
		}
	})

	if !strings.Contains(out, "No callers of NoSuchSymbol") {
		t.Errorf("an empty result printed %q", out)
	}
}

// TestRefsJSONCarriesTheSymbolAndCount pins the machine shape end to end,
// rather than only through the formatter the existing suite tests directly.
func TestRefsJSONCarriesTheSymbolAndCount(t *testing.T) {
	callGraphWorkspace(t)

	out := captureStdout(t, func() {
		if err := runRefs(context.Background(), "Caller", true, false); err != nil {
			t.Fatalf("runRefs: %v", err)
		}
	})

	var payload struct {
		Symbol     string `json:"symbol"`
		Count      int    `json:"count"`
		References []struct {
			Ref      string `json:"ref"`
			Resolved bool   `json:"resolved"`
		} `json:"references"`
	}
	decodeJSON(t, out, &payload)
	if payload.Symbol != "Caller" {
		t.Errorf("symbol = %q, want Caller", payload.Symbol)
	}
	if payload.Count != len(payload.References) {
		t.Errorf("count = %d but %d references were listed", payload.Count, len(payload.References))
	}
	if payload.Count == 0 {
		t.Errorf("no references were recorded for Caller:\n%s", out)
	}
}

// TestRefCommandsNeedAnIndex keeps the missing-index diagnosis consistent with
// every other reader of the index.
func TestRefCommandsNeedAnIndex(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))

	for name, run := range map[string]func() error{
		"callers": func() error { return runCallers(context.Background(), "X", false, false) },
		"refs":    func() error { return runRefs(context.Background(), "X", false, false) },
	} {
		err := run()
		if err == nil {
			t.Errorf("%s with no index returned nil", name)
			continue
		}
		if !strings.Contains(err.Error(), "llm-semantic index") {
			t.Errorf("%s error = %v, want it to name the indexing command", name, err)
		}
	}
}

// TestOpenRefStorageRejectsABackendWithNoCallGraph: the call graph is a SQLite
// feature, so a backend that does not record one must say so rather than
// answering "no callers" for every symbol.
func TestOpenRefStorageRejectsABackendWithNoCallGraph(t *testing.T) {
	callGraphWorkspace(t)
	t.Cleanup(SetStorageTypeForTesting("dynamo"))

	_, _, err := openRefStorage(context.Background())
	if err == nil {
		t.Fatal("an unknown backend was accepted as a call-graph source")
	}
	// It fails at the storage constructor before the interface check, which is
	// the right order: an unopenable backend is the more specific complaint.
	if !strings.Contains(err.Error(), "failed to open index") {
		t.Errorf("error = %v, want it wrapped as an index-open failure", err)
	}
}

// jsTSWorkspace indexes jsTSCallSource under a TypeScript and a JavaScript
// extension, so both languages are exercised through the real index.
func jsTSWorkspace(t *testing.T) {
	t.Helper()
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.ts"), jsTSCallSource)
	// Attribution needs a unique name, so the JavaScript copy renames every
	// symbol; the same names in both files would resolve to neither.
	jsSource := strings.NewReplacer("helper", "jsHelper", "run", "jsRun", "Service", "JsService", "start", "jsStart", "stop", "jsStop").Replace(jsTSCallSource)
	writeFile(t, filepath.Join(dir, "other.js"), jsSource)
	runIndexIn(t, ".", indexOpts{skipCalibration: true})
}

// TestCallersAndRefsAnswerForTypeScriptAndJavaScript defends against the
// regression where TS/JS indexed with no edges, so callers and refs answered
// empty for them. The answer must name real callers and real references.
func TestCallersAndRefsAnswerForTypeScriptAndJavaScript(t *testing.T) {
	jsTSWorkspace(t)

	for _, c := range []struct{ symbol, file, caller string }{
		{"helper", "sample.ts", "run"},
		{"jsHelper", "other.js", "jsRun"},
	} {
		callers := captureStdout(t, func() {
			if err := runCallers(context.Background(), c.symbol, false, false); err != nil {
				t.Fatalf("runCallers: %v", err)
			}
		})
		for _, want := range []string{"2 caller(s) of " + c.symbol, c.file, c.caller} {
			if !strings.Contains(callers, want) {
				t.Errorf("callers of %s lacks %q:\n%s", c.symbol, want, callers)
			}
		}

		refs := captureStdout(t, func() {
			if err := runRefs(context.Background(), c.caller, false, false); err != nil {
				t.Fatalf("runRefs: %v", err)
			}
		})
		if !strings.Contains(refs, c.symbol) || strings.Contains(refs, "not indexed for edges") {
			t.Errorf("refs from %s does not list %s:\n%s", c.caller, c.symbol, refs)
		}
	}
}

// TestCallersOfAPreEdgeJSTSIndexIsNotIndexedForEdges defends the distinction
// between "no callers" and "never recorded": an index built before TS/JS call
// edges existed holds the chunks but no chunk_refs rows, and must say so rather
// than report none.
func TestCallersOfAPreEdgeJSTSIndexIsNotIndexedForEdges(t *testing.T) {
	jsTSWorkspace(t)

	db, err := sql.Open("sqlite", filepath.Join(".index", "semantic.db"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	res, err := db.Exec("DELETE FROM chunk_refs")
	if err != nil {
		t.Fatalf("clear edges: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		t.Fatal("the fixture stored no edges to clear, so the test proves nothing")
	}
	db.Close()
	withAXI(t)

	for name, run := range map[string]func() error{
		"callers": func() error { return runCallers(context.Background(), "helper", false, false) },
		"refs":    func() error { return runRefs(context.Background(), "run", false, false) },
	} {
		out := captureStdout(t, func() {
			if err := run(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		})
		if !strings.Contains(out, "status: not_indexed_for_edges\n") {
			t.Errorf("%s over an edgeless TS/JS index lacks not_indexed_for_edges:\n%s", name, out)
		}
	}
}

// pythonCallSource has two plain functions and a method that calls through
// self. Names are distinct from every other fixture so attribution is unique.
const pythonCallSource = `def py_helper():
    return 1


def py_run():
    return py_helper()


class PyService:
    def py_start(self):
        return self.py_stop()

    def py_stop(self):
        return py_helper()
`

// pythonWorkspace indexes pythonCallSource through the real index.
func pythonWorkspace(t *testing.T) {
	t.Helper()
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.py"), pythonCallSource)
	runIndexIn(t, ".", indexOpts{skipCalibration: true})
}

// TestCallersAndRefsAnswerForPython defends against the regression where
// Python indexed with no edges, so callers and refs answered empty for it. The
// answer must name the real callers and the real references.
func TestCallersAndRefsAnswerForPython(t *testing.T) {
	pythonWorkspace(t)

	callers := captureStdout(t, func() {
		if err := runCallers(context.Background(), "py_helper", false, false); err != nil {
			t.Fatalf("runCallers: %v", err)
		}
	})
	for _, want := range []string{"2 caller(s) of py_helper", "sample.py", "py_run", "py_stop"} {
		if !strings.Contains(callers, want) {
			t.Errorf("callers of py_helper lacks %q:\n%s", want, callers)
		}
	}

	refs := captureStdout(t, func() {
		if err := runRefs(context.Background(), "py_run", false, false); err != nil {
			t.Fatalf("runRefs: %v", err)
		}
	})
	if !strings.Contains(refs, "py_helper") || strings.Contains(refs, "not indexed for edges") {
		t.Errorf("refs from py_run does not list py_helper:\n%s", refs)
	}
}

// TestCallersOfAPrePythonEdgeIndexIsNotIndexedForEdges defends the distinction
// between "no callers" and "never recorded": an index built before Python call
// edges existed holds the chunks but no chunk_refs rows, and must say so rather
// than report none.
func TestCallersOfAPrePythonEdgeIndexIsNotIndexedForEdges(t *testing.T) {
	pythonWorkspace(t)

	db, err := sql.Open("sqlite", filepath.Join(".index", "semantic.db"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	res, err := db.Exec("DELETE FROM chunk_refs")
	if err != nil {
		t.Fatalf("clear edges: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		t.Fatal("the fixture stored no edges to clear, so the test proves nothing")
	}
	db.Close()
	withAXI(t)

	for name, run := range map[string]func() error{
		"callers": func() error { return runCallers(context.Background(), "py_helper", false, false) },
		"refs":    func() error { return runRefs(context.Background(), "py_run", false, false) },
	} {
		out := captureStdout(t, func() {
			if err := run(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		})
		if !strings.Contains(out, "status: not_indexed_for_edges\n") {
			t.Errorf("%s over an edgeless Python index lacks not_indexed_for_edges:\n%s", name, out)
		}
	}
}
