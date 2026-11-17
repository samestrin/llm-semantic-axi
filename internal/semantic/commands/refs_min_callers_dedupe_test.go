package commands

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// These tests defend EST-6878 findings 5 and 6.
//
// Finding 5: `refs --min` printed five fields (file, line, ref, resolved,
// type) under TOON — wider than the four-field default the README calls
// minimal. --min must never be wider than the default.
//
// Finding 6: `callers Helper` printed one row per call site, but every row
// carries the caller's definition line, so two calls from one function read
// as two identical rows. The rows are deduped; reporting the call-site lines
// themselves is a separate change (plan 28's scope).

// TestRefsMinUnderTOONIsNoWiderThanTheDefault: the --min TOON row keeps the
// reference, its resolved flag and (when resolved) the location as path:line
// — three fields, one fewer than the default, which also carries type. An
// external reference keeps two. The total stays.
func TestRefsMinUnderTOONIsNoWiderThanTheDefault(t *testing.T) {
	withAXI(t)
	var buf bytes.Buffer
	if err := formatRefs(&buf, io.Discard, "Run", sampleEdges(), nil, nil, false, true, false); err != nil {
		t.Fatalf("formatRefs --min: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "total: 2\n") || strings.Contains(out, "count:") {
		t.Errorf("--min TOON refs does not state total: 2:\n%s", out)
	}
	if !strings.Contains(out, `file: "a.go:12"`) {
		t.Errorf("--min resolved row does not carry file as path:line:\n%s", out)
	}
	if !strings.Contains(out, "resolved: false") {
		t.Errorf("the external reference lost its resolved flag:\n%s", out)
	}
	for _, absent := range []string{"line:", "type:", "name:"} {
		if strings.Contains(out, absent) {
			t.Errorf("--min refs row is wider than the default, still carries %s:\n%s", absent, out)
		}
	}
}

// doubleCallSource has one function calling Helper twice and a second
// function calling it once: the two call sites in Caller must collapse to one
// row, while Caller2 stays a distinct caller.
const doubleCallSource = `package sample

func Helper() {}

func Caller() {
	Helper()
	Helper()
}

func Caller2() {
	Helper()
}
`

func doubleCallWorkspace(t *testing.T) {
	t.Helper()
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), doubleCallSource)
	captureStdout(t, func() {
		if err := runIndex(context.Background(), ".", indexOpts{skipCalibration: true}); err != nil {
			t.Fatalf("runIndex: %v", err)
		}
	})
}

// TestCallersDedupesIdenticalRows: two calls from the same function are one
// caller, not two identical rows — in plain text, TOON and --json alike, with
// the total counting callers, not call sites.
func TestCallersDedupesIdenticalRows(t *testing.T) {
	doubleCallWorkspace(t)

	plain := captureStdout(t, func() {
		if err := runCallers(context.Background(), "Helper", false, false, false); err != nil {
			t.Fatalf("runCallers: %v", err)
		}
	})
	if !strings.Contains(plain, "2 caller(s) of Helper") {
		t.Errorf("plain callers does not count 2 distinct callers:\n%s", plain)
	}
	if n := strings.Count(plain, "sample.go:"); n != 2 {
		t.Errorf("plain callers printed %d rows, want 2 (one per caller):\n%s", n, plain)
	}

	js := captureStdout(t, func() {
		if err := runCallers(context.Background(), "Helper", true, false, false); err != nil {
			t.Fatalf("runCallers --json: %v", err)
		}
	})
	var payload struct {
		Count   int              `json:"count"`
		Callers []map[string]any `json:"callers"`
	}
	decodeJSON(t, js, &payload)
	if payload.Count != 2 || len(payload.Callers) != 2 {
		t.Errorf("--json callers count/rows = %d/%d, want 2/2:\n%s", payload.Count, len(payload.Callers), js)
	}

	withAXI(t)
	toon := captureStdout(t, func() {
		if err := runCallers(context.Background(), "Helper", false, false, false); err != nil {
			t.Fatalf("runCallers TOON: %v", err)
		}
	})
	if !strings.Contains(toon, "total: 2\n") {
		t.Errorf("TOON callers does not state total: 2:\n%s", toon)
	}
	if !strings.Contains(toon, "callers[2]") {
		t.Errorf("TOON callers does not list exactly 2 rows:\n%s", toon)
	}
}

// TestCallersDedupesASoleCallerToOneRow: the reported case — one function
// calling Helper twice printed two identical rows. It must print one.
func TestCallersDedupesASoleCallerToOneRow(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), `package sample

func Helper() {}

func Caller() {
	Helper()
	Helper()
}
`)
	captureStdout(t, func() {
		if err := runIndex(context.Background(), ".", indexOpts{skipCalibration: true}); err != nil {
			t.Fatalf("runIndex: %v", err)
		}
	})

	plain := captureStdout(t, func() {
		if err := runCallers(context.Background(), "Helper", false, false, false); err != nil {
			t.Fatalf("runCallers: %v", err)
		}
	})
	if !strings.Contains(plain, "1 caller(s) of Helper") {
		t.Errorf("plain callers does not count 1 caller:\n%s", plain)
	}
	if n := strings.Count(plain, "sample.go:"); n != 1 {
		t.Errorf("plain callers printed %d identical rows, want 1:\n%s", n, plain)
	}

	withAXI(t)
	toon := captureStdout(t, func() {
		if err := runCallers(context.Background(), "Helper", false, false, false); err != nil {
			t.Fatalf("runCallers TOON: %v", err)
		}
	})
	if !strings.Contains(toon, "total: 1\n") || !strings.Contains(toon, "callers[1]") {
		t.Errorf("TOON callers does not collapse to one row with total: 1:\n%s", toon)
	}
}
