package commands

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// TestCallersAndRefsNameAChunkWithNoRecordedLanguage defends the NULL-language
// branch of diagnoseZeroEdges: a chunk with no language and no edges must be
// reported as "unknown language" and not indexed for edges, never as "none
// recorded". runIndex always stamps a language, so the test nulls it afterwards.
func TestCallersAndRefsNameAChunkWithNoRecordedLanguage(t *testing.T) {
	pythonWorkspace(t)

	db, err := sql.Open("sqlite", filepath.Join(".index", "semantic.db"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	if _, err := db.Exec("DELETE FROM chunk_refs"); err != nil {
		t.Fatalf("clear edges: %v", err)
	}
	res, err := db.Exec("UPDATE chunks SET language = NULL")
	if err != nil {
		t.Fatalf("null language: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		t.Fatal("the fixture held no chunks to null, so the test proves nothing")
	}
	db.Close()

	runs := map[string]func() error{
		"callers": func() error { return runCallers(context.Background(), "py_helper", false, false) },
		"refs":    func() error { return runRefs(context.Background(), "py_run", false, false) },
	}
	for name, run := range runs {
		plain := captureStdout(t, func() {
			if err := run(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		})
		if !strings.Contains(plain, "unknown language") {
			t.Errorf("plain %s lacks \"unknown language\":\n%s", name, plain)
		}
	}

	withAXI(t)
	for name, run := range runs {
		out := captureStdout(t, func() {
			if err := run(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		})
		for _, want := range []string{"unknown language", "status: not_indexed_for_edges\n"} {
			if !strings.Contains(out, want) {
				t.Errorf("--axi %s lacks %q:\n%s", name, want, out)
			}
		}
	}
}

// failingLangs is a RefStorage with no edges whose SymbolLanguages always errors.
type failingLangs struct{ semantic.RefStorage }

func (failingLangs) GetCallersByName(context.Context, string) ([]semantic.RefEdge, error) {
	return nil, nil
}
func (failingLangs) GetRefsByName(context.Context, string) ([]semantic.RefEdge, error) {
	return nil, nil
}
func (failingLangs) SymbolLanguages(context.Context, string) ([]semantic.SymbolLanguage, error) {
	return nil, errors.New("languages unavailable")
}

// TestZeroEdgeDiagnosisRunsOnlyWhereItsResultIsPrinted defends against a failed
// SymbolLanguages lookup failing --json and --min answers that discard the
// diagnosis, while plain and --axi output, which print it, still surface it.
func TestZeroEdgeDiagnosisRunsOnlyWhereItsResultIsPrinted(t *testing.T) {
	t.Cleanup(func() { GlobalAXIOutput = false })

	cases := []struct {
		name      string
		axi       bool
		json, min bool
		wantErr   bool
	}{
		{"json", false, true, false, false},
		{"min", false, false, true, false},
		{"plain", false, false, false, true},
		{"axi", true, false, false, true},
		{"axi json", true, true, false, true},
		{"axi min", true, false, true, true},
	}
	writers := map[string]func(*bytes.Buffer, bool, bool) error{
		"callers": func(b *bytes.Buffer, j, m bool) error {
			return writeCallers(context.Background(), b, io.Discard, failingLangs{}, "sym", j, m)
		},
		"refs": func(b *bytes.Buffer, j, m bool) error {
			return writeRefs(context.Background(), b, io.Discard, failingLangs{}, "sym", j, m)
		},
	}
	for _, tc := range cases {
		for wname, write := range writers {
			GlobalAXIOutput = tc.axi
			var buf bytes.Buffer
			err := write(&buf, tc.json, tc.min)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "languages unavailable") {
					t.Errorf("%s %s: err = %v, want the lookup error", wname, tc.name, err)
				}
				continue
			}
			if err != nil {
				t.Errorf("%s %s: lookup error surfaced: %v", wname, tc.name, err)
			}
			if !strings.Contains(buf.String(), "sym") {
				t.Errorf("%s %s: answer not printed: %q", wname, tc.name, buf.String())
			}
		}
	}
}

// importsOnly is a RefStorage whose only edge from "Run" is an import, in a
// language that records imports but no calls.
type importsOnly struct{ semantic.RefStorage }

func (importsOnly) GetCallersByName(context.Context, string) ([]semantic.RefEdge, error) {
	return nil, nil
}
func (importsOnly) GetRefsByName(context.Context, string) ([]semantic.RefEdge, error) {
	return []semantic.RefEdge{{ChunkRef: semantic.ChunkRef{RefType: semantic.RefImports, RefName: "json"}}}, nil
}
func (importsOnly) SymbolLanguages(context.Context, string) ([]semantic.SymbolLanguage, error) {
	return []semantic.SymbolLanguage{{Language: "rb", HasEdges: true, HasCallEdges: false}}, nil
}

// TestCallersOfALanguageWithOnlyImportEdgesIsNotIndexedForEdges defends the
// callers diagnosis: an import edge says nothing about calls, so an empty
// callers answer in a language with no call edges must read "not indexed for
// edges", never "recorded in the call graph", which claims none exist.
func TestCallersOfALanguageWithOnlyImportEdgesIsNotIndexedForEdges(t *testing.T) {
	withAXI(t)

	var callers bytes.Buffer
	if err := writeCallers(context.Background(), &callers, io.Discard, importsOnly{}, "Run", false, false); err != nil {
		t.Fatalf("callers: %v", err)
	}
	if !strings.Contains(callers.String(), "status: "+zeroNotIndexedForEdges) {
		t.Errorf("callers of an imports-only language is not %s:\n%s", zeroNotIndexedForEdges, callers.String())
	}

	var refs bytes.Buffer
	if err := writeRefs(context.Background(), &refs, io.Discard, importsOnly{}, "Run", false, false); err != nil {
		t.Fatalf("refs: %v", err)
	}
	if strings.Contains(refs.String(), "calls") || !strings.Contains(refs.String(), "imports") {
		t.Errorf("refs should list the import and claim no call:\n%s", refs.String())
	}
}

// TestCallersAndRefsWriteTheJSONStalenessLineToTheGivenStderr defends the
// --json and --min staleness line going to the stderr writer the caller passed,
// not to the process's stderr, where a test cannot see it and stdout's schema
// would be the only thing checked. importsOnly is no Storage, so its staleness
// is unknown and the line is always printed.
func TestCallersAndRefsWriteTheJSONStalenessLineToTheGivenStderr(t *testing.T) {
	writers := map[string]func(out, errOut *bytes.Buffer, j, m bool) error{
		"callers": func(out, errOut *bytes.Buffer, j, m bool) error {
			return writeCallers(context.Background(), out, errOut, importsOnly{}, "Run", j, m)
		},
		"refs": func(out, errOut *bytes.Buffer, j, m bool) error {
			return writeRefs(context.Background(), out, errOut, importsOnly{}, "Run", j, m)
		},
	}
	for name, write := range writers {
		for _, mode := range []struct {
			name      string
			json, min bool
		}{{"json", true, false}, {"min", false, true}} {
			var out, errOut bytes.Buffer
			if err := write(&out, &errOut, mode.json, mode.min); err != nil {
				t.Fatalf("%s --%s: %v", name, mode.name, err)
			}
			if !strings.Contains(errOut.String(), "Index staleness unknown") {
				t.Errorf("%s --%s: staleness line not on the given stderr: %q", name, mode.name, errOut.String())
			}
			if strings.Contains(out.String(), "staleness") {
				t.Errorf("%s --%s: staleness line leaked into stdout: %q", name, mode.name, out.String())
			}
		}
	}
}

// TestZeroCallersInALanguageWithOnlyImportEdges defends the callers wording
// for a language whose chunks record import or type edges but no call edges:
// it said "the index records no edges for it" and "its java chunks are not
// indexed for edges", both false when the index holds that language's import
// edges. callers answers from call edges, so it must say no CALL edges.
func TestZeroCallersInALanguageWithOnlyImportEdges(t *testing.T) {
	ctx := context.Background()
	storage, err := semantic.NewSQLiteStorage(filepath.Join(t.TempDir(), "semantic.db"), 4)
	if err != nil {
		t.Fatalf("NewSQLiteStorage: %v", err)
	}
	t.Cleanup(func() {
		if err := storage.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	emb := []float32{0.1, 0.2, 0.3, 0.4}
	for _, c := range []semantic.Chunk{
		{ID: "java-widget", FilePath: "/r/Widget.java", Type: semantic.ChunkFunction, Name: "Widget", Language: "java"},
		{ID: "java-user", FilePath: "/r/User.java", Type: semantic.ChunkFunction, Name: "User", Language: "java"},
		{ID: "go-widget", FilePath: "/r/widget.go", Type: semantic.ChunkFunction, Name: "Gadget", Language: "go"},
		{ID: "go-caller", FilePath: "/r/caller.go", Type: semantic.ChunkFunction, Name: "Caller", Language: "go"},
	} {
		if err := storage.Create(ctx, c, emb); err != nil {
			t.Fatalf("Create %s: %v", c.ID, err)
		}
	}
	if err := storage.StoreRefs(ctx, []semantic.ChunkRef{
		{ChunkID: "java-user", RefType: semantic.RefImports, RefName: "Widget"},
		{ChunkID: "go-caller", RefType: semantic.RefCalls, RefName: "Other"},
	}); err != nil {
		t.Fatalf("StoreRefs: %v", err)
	}

	zero, err := diagnoseZeroEdges(ctx, storage, "Widget", "callers of", true, 0)
	if err != nil {
		t.Fatalf("diagnoseZeroEdges: %v", err)
	}
	if zero.Status != zeroNotIndexedForEdges {
		t.Errorf("status = %q, want %q", zero.Status, zeroNotIndexedForEdges)
	}
	if strings.Contains(zero.Message, "records no edges") || !strings.Contains(zero.Message, "records no call edges") {
		t.Errorf("message %q must say java records no call edges, not no edges", zero.Message)
	}

	// The same name in Go, which records call edges, and in Java.
	if err := storage.Create(ctx, semantic.Chunk{ID: "go-widget2", FilePath: "/r/w2.go", Type: semantic.ChunkFunction, Name: "Widget", Language: "go"}, emb); err != nil {
		t.Fatalf("Create: %v", err)
	}
	zero, err = diagnoseZeroEdges(ctx, storage, "Widget", "callers of", true, 0)
	if err != nil {
		t.Fatalf("diagnoseZeroEdges: %v", err)
	}
	if zero.Status != zeroNoneRecorded {
		t.Errorf("status = %q, want %q", zero.Status, zeroNoneRecorded)
	}
	if strings.Contains(zero.Message, "not indexed for edges") || !strings.Contains(zero.Message, "its java chunks record no call edges") {
		t.Errorf("message %q must say the java chunks record no call edges", zero.Message)
	}

	// refs counts any edge, so java's import edge is edge support there.
	zero, err = diagnoseZeroEdges(ctx, storage, "Widget", "references from", false, 0)
	if err != nil {
		t.Fatalf("diagnoseZeroEdges: %v", err)
	}
	if zero.Message != "No references from Widget recorded in the call graph" {
		t.Errorf("refs message = %q, want none recorded with no caveat", zero.Message)
	}
}
