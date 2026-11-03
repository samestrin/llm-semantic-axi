package treesitter

import (
	"sort"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// refRows extracts edges through the registered factory chunker for path and
// renders them as "chunk ref_type ref_name", sorted.
func refRows(t *testing.T, path, src string) []string {
	t.Helper()
	factory := semantic.NewChunkerFactory()
	RegisterAll(factory)
	c, ok := factory.GetByExtension(path)
	if !ok {
		t.Fatalf("no chunker for %s", path)
	}
	re, ok := c.(semantic.RefExtractor)
	if !ok {
		t.Fatalf("registered chunker %T for %s does not extract refs", c, path)
	}
	chunks, err := c.Chunk(path, []byte(src))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	refs, err := re.ExtractRefs(path, []byte(src), chunks)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	names := map[string]string{}
	for _, ch := range chunks {
		names[ch.ID] = ch.Name
	}
	var rows []string
	for _, r := range refs {
		rows = append(rows, names[r.ChunkID]+" "+string(r.RefType)+" "+r.RefName)
	}
	sort.Strings(rows)
	return rows
}

func assertRows(t *testing.T, got []string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("edges:\n  got  %q\n  want %q", got, want)
	}
}

// Defends "a class does not call what its methods call" where line ranges
// alone cannot tell them apart: on one line, the class and its method span
// the same lines, and the call must still go to the method.
func TestJSRefs_OneLineClassAttributesTheCallToTheMethod(t *testing.T) {
	assertRows(t, refRows(t, "a.js", "class A { m() { go(); } }\n"), "m calls go")
}

// Defends the TSX grammar choice: JSX in a .tsx file parsed with the plain
// TypeScript grammar is a parse error, and the call inside the component
// would be lost to a text window.
func TestTSRefs_TSXComponentCallsAreRecorded(t *testing.T) {
	src := "export function Button() {\n    track();\n    return <div onClick={() => save()}>hi</div>;\n}\n"
	assertRows(t, refRows(t, "b.tsx", src), "Button calls track", "Button calls save")
}

// Defends against an invented edge: a call whose callee has no name (a call
// result, a computed member) is not recorded under some guessed name, and
// arrow-function chunks and default imports are attributed like any other.
func TestJSRefs_CalleesWithoutANameAreSkipped(t *testing.T) {
	src := `import React from "react";
import "./side-effect.js";

const run = () => {
    make()();
    table[key]();
    obj.get();
    a.b.c();
};
`
	assertRows(t, refRows(t, "c.ts", src),
		"run imports react",
		"run imports side-effect",
		"run calls make",
		"run calls obj.get",
		"run calls c",
	)
}

// Defends the fallback: calls in lines the parser could not parse are not
// attributed to the text windows standing in for them, so a broken file
// yields edges only for the code that parsed.
func TestJSRefs_BrokenFileRecordsEdgesOnlyForParsedChunks(t *testing.T) {
	src := "function ok() {\n    helper();\n}\n\nwhile (true { broken(); }\n"
	rows := refRows(t, "d.js", src)
	assertRows(t, rows, "ok calls helper")
}

// Defends against a file with no chunk at all producing an edge with an
// empty chunk ID, which no query could ever return.
func TestJSRefs_NoChunksNoEdges(t *testing.T) {
	c := NewJSChunker()
	refs, err := c.ExtractRefs("e.js", []byte("import x from 'y';\nfoo();\n"), nil)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("got %d edges for a file with no chunks: %+v", len(refs), refs)
	}
}
