package commands

import (
	"context"
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
