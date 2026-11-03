package commands

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
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
			return writeCallers(context.Background(), b, failingLangs{}, "sym", j, m)
		},
		"refs": func(b *bytes.Buffer, j, m bool) error {
			return writeRefs(context.Background(), b, failingLangs{}, "sym", j, m)
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
