package treesitter

import (
	"context"
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
// result, a computed member) is not recorded under some guessed name, nor is
// a member call on any receiver but `this` (epic 03.1 T2), and arrow-function
// chunks and default imports are attributed like any other.
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
	)
}

// Defends against a guessed edge (epic 03.1 T2): the resolver links a call on
// the text after its last dot, so `obj.get()` recorded under any name would
// link to whichever lone chunk is called `get`, though nothing says obj is
// that chunk's owner. Only a bare name or a `this.` receiver is recorded, in
// both grammars.
func TestJSRefs_OnlyBareAndThisCallsAreRecorded(t *testing.T) {
	src := `function get() {}

class Store {
    #drop() {}
    load() {
        get();
        this.get();
        this?.get();
        this.#drop();
        obj.get();
        obj?.get();
        super.get();
        this.cache.get();
        obj[k]();
        obj["get"]();
        make()();
        (cond ? a : b)();
    }
}
`
	for _, path := range []string{"s.ts", "s.js"} {
		t.Run(path, func(t *testing.T) {
			assertRows(t, refRows(t, path, src),
				"load calls get",
				"load calls make",
				"load calls this.#drop",
				"load calls this.get",
				"load calls this.get",
			)
		})
	}
}

// Defends the done-when of epic 03.1 T2 where it bites: through a real sqlite
// store and the resolver, with a lone `get` chunk indexed, a member call on
// another receiver and a dynamic call leave `get` with no caller, while a
// `this.get()` in the same file still resolves (so the fixture can link).
func TestJSRefs_MemberAndDynamicCallsDoNotLinkToALoneChunk(t *testing.T) {
	ctx := context.Background()
	storage, err := semantic.NewSQLiteStorage(":memory:", 0)
	if err != nil {
		t.Fatalf("NewSQLiteStorage: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	files := map[string]string{
		"util.ts": "export function get() {}\n",
		"app.js": `function run() {
    obj.get();
    obj[k]();
    obj.get()();
}

function viaThis() {
    this.get();
}
`,
	}
	factory := semantic.NewChunkerFactory()
	RegisterAll(factory)
	for path, src := range files {
		c, _ := factory.GetByExtension(path)
		chunks, err := c.Chunk(path, []byte(src))
		if err != nil {
			t.Fatalf("Chunk %s: %v", path, err)
		}
		for _, ch := range chunks {
			if err := storage.Create(ctx, ch, nil); err != nil {
				t.Fatalf("Create %s: %v", ch.Name, err)
			}
		}
		refs, err := c.(semantic.RefExtractor).ExtractRefs(path, []byte(src), chunks)
		if err != nil {
			t.Fatalf("ExtractRefs %s: %v", path, err)
		}
		if err := storage.StoreRefs(ctx, refs); err != nil {
			t.Fatalf("StoreRefs %s: %v", path, err)
		}
	}
	if err := storage.ResolveRefs(ctx); err != nil {
		t.Fatalf("ResolveRefs: %v", err)
	}

	edges, err := storage.GetCallersByName(ctx, "get")
	if err != nil {
		t.Fatalf("GetCallersByName: %v", err)
	}
	var callers []string
	for _, e := range edges {
		callers = append(callers, e.Name+" "+e.RefName)
	}
	assertRows(t, callers, "viaThis this.get")

	runRefs, err := storage.GetRefsByName(ctx, "run")
	if err != nil {
		t.Fatalf("GetRefsByName: %v", err)
	}
	if len(runRefs) != 0 {
		t.Errorf("run wrote %d edges for calls with no resolvable callee: %+v", len(runRefs), runRefs)
	}
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
