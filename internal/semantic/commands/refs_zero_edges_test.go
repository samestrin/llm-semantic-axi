package commands

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
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
