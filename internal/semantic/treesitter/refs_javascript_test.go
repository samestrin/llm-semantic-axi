package treesitter

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/v2/internal/semantic"
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
// `this.get()` in a class that has its own get still resolves (so the fixture
// can link). viaThis was once a top-level function whose this.get() linked to
// util.ts's get, which is the false edge epic 03.1 F1 removed: a `this.` call
// links only within the caller's own class.
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

class Box {
    get() {}

    viaThis() {
        this.get();
    }
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

// Defends debt row 8f24ba4c20057d5a (epic 37 T2): `require("x")` was recorded
// as `calls require`, so `callers require` listed every CommonJS file. Each
// require with a static module path is an `imports` edge instead, named like
// any other import, and `require` never appears as a callee. Imports go to
// the first chunk, as static imports and Go's do.
func TestJSRefs_RequireRecordsImportsNotCalls(t *testing.T) {
	src := readTestFile(t, "javascript", "commonjs.cjs")
	assertRows(t, refRows(t, "commonjs.cjs", string(src)),
		"loadConfig imports fs",
		"loadConfig imports io",
		"loadConfig imports a",
	)
}

// Defends debt row 4806efa8cea3005f (epic 37 T2): dynamic `import("./m.js")`
// wrote no edge at all, so a code-split module never appeared in `refs`. A
// static argument records an `imports` edge; the surrounding call chain
// (`.then(m => m.go())`) still records nothing it cannot name.
func TestJSRefs_DynamicImportRecordsImports(t *testing.T) {
	src := readTestFile(t, "javascript", "dynamic-import.js")
	assertRows(t, refRows(t, "dynamic-import.js", string(src)),
		"boot imports m",
		"boot imports react",
	)
}

// Defends debt row 1c0963ce2274ce91 (epic 37 T2): re-exports (`export ...
// from`) wrote no `imports` edge, so a facade module showed no dependency on
// what it re-exports. Every export_statement with a source records one,
// including `export *`, `export * as ns` and TS `export type`.
func TestTSRefs_ReExportsRecordImports(t *testing.T) {
	src := readTestFile(t, "typescript", "reexports.ts")
	assertRows(t, refRows(t, "reexports.ts", string(src)),
		"facade imports a",
		"facade imports b",
		"facade imports c",
		"facade imports pkg",
		"facade imports types",
		"facade calls a",
	)
}

// Defends debt row 83e7bda1fece283c (epic 37 T2): a dotted file name such as
// `./a.test` was kept as `a.test`. Imports are never resolved to chunks; the
// name is what `refs` displays (and dedupes on), so the dotted suffix showed
// up in the output. The name is the last path segment up to its first dot,
// so `./a.test` is `a` and `./b.test.js` is `b`, in every import form.
func TestJSRefs_DottedImportSuffixIsStripped(t *testing.T) {
	src := `import "./a.test";
import "./b.test.js";

function f() {
    require("./c.spec.ts");
    import("./d.test.mjs");
}
`
	for _, path := range []string{"n.js", "n.ts"} {
		t.Run(path, func(t *testing.T) {
			assertRows(t, refRows(t, path, src),
				"f imports a",
				"f imports b",
				"f imports c",
				"f imports d",
			)
		})
	}
}

// Defends against an invented edge (epic 37 T2): a require or dynamic import
// whose module cannot be known statically (a variable, no argument, several
// arguments, a template with a substitution) records nothing, and neither
// does a `require` reached through an object. None of them may fall back to
// `calls require` or `calls import`. A call inside the argument is still a
// call and keeps its edge: the module-load branch must not swallow the walk.
func TestJSRefs_NonStaticModuleLoadsRecordNothing(t *testing.T) {
	src := `function f() {
    require(x);
    require();
    require("a", "b");
    obj.require("x");
    import(expr);
    import();
    import("a", "b");
    import(` + "`./m${n}.js`" + `);
    require(` + "`./m${n}.js`" + `);
    import(getPath());
    require(getPath());
}
`
	for _, path := range []string{"q.js", "q.ts"} {
		t.Run(path, func(t *testing.T) {
			assertRows(t, refRows(t, path, src),
				"f calls getPath",
				"f calls getPath",
			)
		})
	}
}

// Defends the substitution-free template case of epic 37 T2: a template
// literal with no `${...}` is a static module path, so `import()` and
// `require()` record it like a quoted string.
func TestJSRefs_PlainTemplateModuleLoadsRecordImports(t *testing.T) {
	src := "function f() {\n    require(`./util/io.js`);\n    import(`./m.test`);\n}\n"
	assertRows(t, refRows(t, "tpl.js", src),
		"f imports io",
		"f imports m",
	)
}

// Defends the jsImportName rule (epic 37 T2, epic 40 T4 / debt row
// c3a446bad584e404): only path sources (./, ../, /) get their module
// suffixes (.js .mjs .cjs .jsx .ts .tsx .mts .cts .json .test .spec)
// repeatedly stripped from the final segment; a bare package specifier
// keeps its dots (`lodash.debounce` was cut to `lodash`), and the dotfile
// guard keeps a leading-dot name whole rather than stripping it to nothing.
func TestJSImportName(t *testing.T) {
	cases := map[string]string{
		"\"./util/io.js\"":    "io",
		"'./a.test'":          "a",
		"'./b.test.js'":       "b",
		"\"react\"":           "react",
		"lodash/debounce":     "debounce",
		"@scope/pkg":          "pkg",
		"'./.hidden'":         ".hidden",
		"\"\"":                "",
		"./jquery.min.js":     "jquery.min",
		"./vue.config.js":     "vue.config",
		"./a.spec":            "a",
		"./a.test.spec.js":    "a",
		"./a.spec.test.ts":    "a",
		"./m.mjs":             "m",
		"./c.cjs":             "c",
		"./x.jsx":             "x",
		"./t.ts":              "t",
		"./x.tsx":             "x",
		"./m.mts":             "m",
		"./c.cts":             "c",
		"./data.json":         "data",
		"./a.js.js":           "a",
		"../util/io.js":       "io",
		"../b.test.ts":        "b",
		"../../c.spec.mjs":    "c",
		"/abs/path/m.mjs":     "m",
		"/a.test.js":          "a",
		"'../vue.config.js'":  "vue.config",
		"lodash.debounce":     "lodash.debounce",
		"socket.io":           "socket.io",
		"chart.js":            "chart.js",
		"vue.config.js":       "vue.config.js",
		"\"lodash.debounce\"": "lodash.debounce",
		"@scope/pkg.js":       "pkg.js",
		"pkg/sub.js":          "sub.js",
		"./.test":             ".test",
		"./.spec.js":          ".spec",
		"./.hidden.js":        ".hidden",
		"./a.min":             "a.min",
		"./a.testx":           "a.testx",
	}
	for source, want := range cases {
		if got := jsImportName(source); got != want {
			t.Errorf("jsImportName(%q) = %q, want %q", source, got, want)
		}
	}
}

// Defends debt row 544b5ce505b7ccbc (epic 37 T3): `new Foo()` recorded no
// edge, so `callers Foo` missed every construction. A bare constructor name
// records a `calls` edge, with or without the argument list (`new Bar`
// constructs too), and a call inside the arguments keeps its own edge.
func TestJSRefs_NewBareConstructorRecordsCalls(t *testing.T) {
	src := `function build() {
    new Foo();
    new Bar;
    new Foo(make());
}
`
	for _, path := range []string{"n.js", "n.ts"} {
		t.Run(path, func(t *testing.T) {
			assertRows(t, refRows(t, path, src),
				"build calls Bar",
				"build calls Foo",
				"build calls Foo",
				"build calls make",
			)
		})
	}
	// TS type arguments sit between the name and the argument list; the
	// constructor is still a bare identifier.
	assertRows(t, refRows(t, "g.ts", "function f() { new Map<string, number>(); }\n"), "f calls Map")
}

// Defends the owner's answer to EST-8928 Q3 (epic 37 T3): only a bare
// identifier constructor is recorded. `new a.Foo()` stays unrecorded — the
// resolver links on the text after the last dot, so it would link to any
// lone chunk named Foo though nothing ties a to it — and so do `new
// this.Foo()` (the answer says "bare identifier only"), a computed
// constructor and a parenthesized one. Calls inside those expressions keep
// their own edges: the new_expression case must not swallow the walk.
func TestJSRefs_NewWithReceiverOrUnnamedConstructorIsSkipped(t *testing.T) {
	src := `function build() {
    new a.Foo();
    new a.b.Foo();
    new this.Foo();
    new obj[k]();
    new (getFactory())();
    new make()();
}
`
	for _, path := range []string{"r.js", "r.ts"} {
		t.Run(path, func(t *testing.T) {
			assertRows(t, refRows(t, path, src),
				"build calls getFactory",
				"build calls make",
			)
		})
	}
}

// Defends the one recursion rule (epic 37 T1) on the new edge: `new Widget()`
// inside function Widget is self-construction and writes no edge, while the
// same bare name inside a method named Widget constructs the free Widget and
// keeps its edge, exactly as a bare call does.
func TestJSRefs_NewSelfConstructionFollowsRecursionRule(t *testing.T) {
	assertRows(t, refRows(t, "rec.js", "function Widget() {\n    new Widget();\n}\n"))
	assertRows(t, refRows(t, "m.js", "class C {\n    Widget() {\n        new Widget();\n    }\n}\n"), "Widget calls Widget")
}

// Defends the done-when of epic 37 T3 through a real sqlite store and the
// resolver: `callers Foo` lists the constructing function. A construction
// through an object (`new a.Foo()`) must not link to the lone Foo chunk.
func TestJSRefs_CallersFooListsTheConstructingFunction(t *testing.T) {
	ctx := context.Background()
	storage, err := semantic.NewSQLiteStorage(":memory:", 0)
	if err != nil {
		t.Fatalf("NewSQLiteStorage: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	files := map[string]string{
		"foo.ts": "export class Foo {}\n",
		"app.js": `function build() {
    new Foo();
}

function elsewhere() {
    new a.Foo();
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

	edges, err := storage.GetCallersByName(ctx, "Foo")
	if err != nil {
		t.Fatalf("GetCallersByName: %v", err)
	}
	var callers []string
	for _, e := range edges {
		callers = append(callers, e.Name+" "+e.RefName)
	}
	assertRows(t, callers, "build Foo")
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

// Defends debt row 132c8866610ca361 (epic 40 T2): a call at file scope lay
// outside every function chunk, so no edge was written and `callers init`
// came back empty though the file calls it. It now goes to the `<module>`
// chunk, and only to it: `init` does not call itself, so the recursion rule
// must not turn the module's call into `init calls init`.
func TestJSRefs_TopLevelCallGoesToModule(t *testing.T) {
	src := "function init() {}\n\ninit();\n"
	for _, path := range []string{"m.js", "m.ts"} {
		t.Run(path, func(t *testing.T) {
			assertRows(t, refRows(t, path, src), "<module> calls init")
		})
	}
}

// Defends against `<module>` swallowing calls a narrower chunk holds: a call
// inside a function or method stays with it, while the file-scope call beside
// it goes to `<module>`. The one-line file is the tie case: the module spans
// the same single line as the function, and the function must still win.
func TestJSRefs_CallInsideAFunctionStaysWithTheFunction(t *testing.T) {
	src := "function a() {\n    b();\n}\n\nclass C {\n    m() {\n        b();\n    }\n}\n\nb();\n"
	assertRows(t, refRows(t, "f.js", src), "a calls b", "m calls b", "<module> calls b")
	assertRows(t, refRows(t, "one.js", "function f() { g(); }"), "f calls g")
}

// Defends the fallback rule on the new owner: a file that did not parse has
// no `<module>` chunk, and a call at file scope in it is not handed to a text
// window instead, since the tree there is not reliable.
func TestJSRefs_TopLevelCallInABrokenFileRecordsNothing(t *testing.T) {
	src := "function ok() {\n    helper();\n}\n\ntop();\n\nwhile (true { broken(); }\n"
	assertRows(t, refRows(t, "d.js", src), "ok calls helper")
}

// Defends that only the parsed `<module>` chunk owns file-scope calls: a
// whole-file ChunkFile window with any other name (what the fallback emits)
// gets no edge, while one named `<module>` gets it.
func TestJSRefs_OnlyTheModuleChunkOwnsFileScopeCalls(t *testing.T) {
	src := []byte("top();\n")
	window := semantic.Chunk{ID: "w", Type: semantic.ChunkFile, Name: "d.js", StartLine: 1, EndLine: 1}
	refs, err := NewJSChunker().ExtractRefs("d.js", src, []semantic.Chunk{window})
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("a fallback window got edges: %+v", refs)
	}
	module := semantic.Chunk{ID: "m", Type: semantic.ChunkFile, Name: moduleChunkName, StartLine: 1, EndLine: 1}
	refs, err = NewJSChunker().ExtractRefs("d.js", src, []semantic.Chunk{window, module})
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	if len(refs) != 1 || refs[0].ChunkID != "m" || refs[0].RefName != "top" {
		t.Errorf("want one edge m calls top, got %+v", refs)
	}
}
