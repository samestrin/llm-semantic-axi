package commands

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// These tests defend EST-6829 finding 1: the default TOON answer of callers
// and refs must meet the same AXI shape the README promises for every command
// — at most four fields per row and a stated total — with --full restoring
// every field. Before the fix, callers rows carried five fields, refs rows
// six, and both said `count`, an undeclared deviation from principles 2 and 4.
// --json keeps its pre-AXI schema (board decision, 2026-10-31): `count` and
// every field, untouched by the shaping.

// toonHeaderFields returns the field set of a uniform TOON row list, the
// `name[N]{a,b,c}:` header go-axi prints when every row carries the same keys.
func toonHeaderFields(t *testing.T, out, list string) map[string]bool {
	t.Helper()
	marker := list + "["
	i := strings.Index(out, marker)
	if i < 0 {
		t.Fatalf("output has no %s list:\n%s", list, out)
	}
	rest := out[i+len(marker):]
	open := strings.Index(rest, "{")
	close := strings.Index(rest, "}")
	if open < 0 || close < open || !strings.Contains(rest[:open], "]") {
		t.Fatalf("%s list has no uniform-row header:\n%s", list, out)
	}
	fields := map[string]bool{}
	for _, f := range strings.Split(rest[open+1:close], ",") {
		fields[strings.TrimSpace(f)] = true
	}
	return fields
}

func assertFieldSet(t *testing.T, got map[string]bool, want []string, context string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: fields = %v, want exactly %v", context, got, want)
		return
	}
	for _, f := range want {
		if !got[f] {
			t.Errorf("%s: missing field %q in %v", context, f, got)
		}
	}
}

// TestCallersAXIDefaultIsShapedAndStatesTotal: the default TOON callers row
// keeps the caller's location, name and the written reference — three fields,
// with `file` as path:line like a search row — and the payload states `total`,
// not `count`. The edge type is always `calls` for callers, so it is what
// --full restores, not what the default spends tokens on.
func TestCallersAXIDefaultIsShapedAndStatesTotal(t *testing.T) {
	callGraphWorkspace(t)

	out := captureStdout(t, func() {
		if _, err := executeCommand(t, RootCmd(), "callers", "Authenticate"); err != nil {
			t.Fatalf("callers: %v", err)
		}
	})

	if !strings.Contains(out, "total: 1\n") {
		t.Errorf("default TOON callers does not state total: 1:\n%s", out)
	}
	if strings.Contains(out, "count:") {
		t.Errorf("default TOON callers still says count:\n%s", out)
	}
	assertFieldSet(t, toonHeaderFields(t, out, "callers"), []string{"file", "name", "ref"}, "default callers row")
	if !strings.Contains(out, "Caller") {
		t.Errorf("the calling function is missing:\n%s", out)
	}
}

// TestRefsAXIDefaultIsShapedAndStatesTotal: a resolved refs row keeps four
// fields (file as path:line, ref, resolved, type); an external reference keeps
// three (ref, resolved, type) because it has no location. `resolved` stays in
// the default: dropping it would make an external reference read as an
// indexed one with a missing file. The referring chunk's name is the queried
// symbol, already stated once in the payload, so --full is where it lives.
func TestRefsAXIDefaultIsShapedAndStatesTotal(t *testing.T) {
	withAXI(t)
	var buf bytes.Buffer
	if err := formatRefs(&buf, io.Discard, "Run", sampleEdges(), nil, nil, false, false, false); err != nil {
		t.Fatalf("formatRefs: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "total: 2\n") {
		t.Errorf("default TOON refs does not state total: 2:\n%s", out)
	}
	if strings.Contains(out, "count:") {
		t.Errorf("default TOON refs still says count:\n%s", out)
	}
	if strings.Contains(out, "name:") {
		t.Errorf("default TOON refs row still carries the redundant name:\n%s", out)
	}
	if strings.Contains(out, "line:") {
		t.Errorf("default TOON refs row still carries a separate line key:\n%s", out)
	}
	if !strings.Contains(out, `file: "a.go:12"`) {
		t.Errorf("resolved refs row does not carry file as path:line:\n%s", out)
	}
	if !strings.Contains(out, "resolved: false") {
		t.Errorf("the external reference lost its resolved flag:\n%s", out)
	}
}

// TestCallersAXIFullRestoresEveryField: --full is the escape hatch the AXI
// shape promises — every field the row ever carried, with the total kept.
func TestCallersAXIFullRestoresEveryField(t *testing.T) {
	callGraphWorkspace(t)

	out := captureStdout(t, func() {
		if _, err := executeCommand(t, RootCmd(), "callers", "--full", "Authenticate"); err != nil {
			t.Fatalf("callers --full: %v", err)
		}
	})

	if !strings.Contains(out, "total: 1\n") {
		t.Errorf("--full callers does not state total: 1:\n%s", out)
	}
	assertFieldSet(t, toonHeaderFields(t, out, "callers"), []string{"file", "line", "name", "ref", "type"}, "--full callers row")
}

// TestRefsAXIFullRestoresEveryField: --full restores the separate line key
// and the referring chunk's name that the default row drops.
func TestRefsAXIFullRestoresEveryField(t *testing.T) {
	callGraphWorkspace(t)

	out := captureStdout(t, func() {
		if _, err := executeCommand(t, RootCmd(), "refs", "--full", "Caller"); err != nil {
			t.Fatalf("refs --full: %v", err)
		}
	})

	if !strings.Contains(out, "total: 1\n") {
		t.Errorf("--full refs does not state total: 1:\n%s", out)
	}
	assertFieldSet(t, toonHeaderFields(t, out, "references"), []string{"file", "line", "name", "ref", "resolved", "type"}, "--full refs row")
	if strings.Contains(out, "count:") {
		t.Errorf("--full refs still says count:\n%s", out)
	}
}

// TestRefCommandsHaveFullFlag: the shaping is only honest if the dropped
// fields stay reachable, so both commands must accept --full.
func TestRefCommandsHaveFullFlag(t *testing.T) {
	for _, c := range RootCmd().Commands() {
		name := strings.Fields(c.Use)[0]
		if name != "callers" && name != "refs" {
			continue
		}
		if c.Flags().Lookup("full") == nil {
			t.Errorf("%s has no --full flag", name)
		}
	}
}

// TestCallersAndRefsJSONKeepCountAndFullFields: the board kept --json's
// pre-AXI schema (2026-10-31). The TOON shaping must not leak into it:
// `count`, the separate line key and every field stay exactly as they were.
func TestCallersAndRefsJSONKeepCountAndFullFields(t *testing.T) {
	withPlain(t)

	var callers bytes.Buffer
	if err := formatCallers(&callers, io.Discard, "Process", sampleEdges()[:1], nil, nil, true, false, false); err != nil {
		t.Fatalf("formatCallers: %v", err)
	}
	var payload struct {
		Count   int `json:"count"`
		Callers []struct {
			File string `json:"file"`
			Line int    `json:"line"`
			Name string `json:"name"`
			Ref  string `json:"ref"`
			Type string `json:"type"`
		} `json:"callers"`
	}
	decodeJSON(t, callers.String(), &payload)
	if payload.Count != 1 || len(payload.Callers) != 1 {
		t.Fatalf("--json callers lost its count or row:\n%s", callers.String())
	}
	row := payload.Callers[0]
	if row.File != "a.go" || row.Line != 12 || row.Name != "Run" || row.Ref != "w.Process" || row.Type != "calls" {
		t.Errorf("--json callers row changed: %+v", row)
	}

	var refs bytes.Buffer
	if err := formatRefs(&refs, io.Discard, "Run", sampleEdges(), nil, nil, true, false, false); err != nil {
		t.Fatalf("formatRefs: %v", err)
	}
	var refsPayload struct {
		Count      int `json:"count"`
		References []struct {
			Ref      string `json:"ref"`
			Type     string `json:"type"`
			Resolved bool   `json:"resolved"`
			File     string `json:"file"`
			Line     int    `json:"line"`
			Name     string `json:"name"`
		} `json:"references"`
	}
	decodeJSON(t, refs.String(), &refsPayload)
	if refsPayload.Count != 2 || len(refsPayload.References) != 2 {
		t.Fatalf("--json refs lost its count or rows:\n%s", refs.String())
	}
	resolved := refsPayload.References[0]
	if resolved.File != "a.go" || resolved.Line != 12 || resolved.Name != "Run" || !resolved.Resolved {
		t.Errorf("--json refs resolved row changed: %+v", resolved)
	}
}

// TestCallersAndRefsMinUnderTOONStateTotal: --min keeps its own compact rows,
// but TOON output always states a total (principle 4), as the search family's
// --min already does. Under --json the compact payload keeps `count`.
func TestCallersAndRefsMinUnderTOONStateTotal(t *testing.T) {
	withAXI(t)

	var callers bytes.Buffer
	if err := formatCallers(&callers, io.Discard, "Process", sampleEdges()[:1], nil, nil, false, true, false); err != nil {
		t.Fatalf("formatCallers: %v", err)
	}
	if !strings.Contains(callers.String(), "total: 1\n") || strings.Contains(callers.String(), "count:") {
		t.Errorf("--min TOON callers does not state total:\n%s", callers.String())
	}
	assertFieldSet(t, toonHeaderFields(t, callers.String(), "callers"), []string{"file", "line", "name"}, "--min callers row")

	var refs bytes.Buffer
	if err := formatRefs(&refs, io.Discard, "Run", sampleEdges(), nil, nil, false, true, false); err != nil {
		t.Fatalf("formatRefs: %v", err)
	}
	if !strings.Contains(refs.String(), "total: 2\n") || strings.Contains(refs.String(), "count:") {
		t.Errorf("--min TOON refs does not state total:\n%s", refs.String())
	}
}
