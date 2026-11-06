package treesitter

import (
	"context"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// rustRefsFixture covers each shape epic 03.2 T1 names: a direct call, a
// self. method call, use imports (a plain path, an alias, a {a, b} list with
// self, a wildcard, and one inside a function), an impl whose methods make
// calls the impl itself does not, and calls and a use no function chunk holds
// (an impl const, a static, the body of an inline mod).
const rustRefsFixture = `use std::collections::HashMap;
use crate::store::{self, load, save as persist};
use crate::prelude::*;

pub struct Config {
    items: HashMap<String, u32>,
}

impl Repo {
    const LIMIT: usize = limit();

    pub fn get(&self) -> u32 {
        self.fetch();
        helper()
    }

    fn fetch(&self) {
        self.fetch();
    }
}

fn helper() -> u32 {
    use std::fmt::Write;
    helper();
    0
}

static TABLE: u32 = compute();

mod inner {
    use other::thing;

    fn hidden() {
        buried();
    }
}
`

// Defends the epic 03.2 T1 success line: a Rust file indexed the way the CLI
// indexes it (through the registered, Fallback-wrapped chunker) stores call
// and import edges. Before T1 RustChunker had no ExtractRefs, so a Rust index
// held no edges at all and callers/refs answered empty. It also pins the ways
// copying GoChunker would go wrong for Rust: a field access is not a type use,
// so no uses_type row; an impl spans its methods, so attributing by line range
// would make Repo a caller of everything get calls; and a call in an impl
// const, a static or an inline mod body has no function to belong to, so it
// writes no edge rather than landing on the impl, the first chunk or the mod.
func TestRustRefs_IndexedThroughTheRegisteredChunker(t *testing.T) {
	storage, mgr, dir := newRefIndex(t, "rs")
	writePython(t, dir, "repo.rs", rustRefsFixture)
	if _, err := mgr.Index(context.Background(), dir, semantic.IndexOptions{}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	got := storedRefs(t, storage)
	want := map[string][]string{
		// Top-level imports go to the file's first chunk, as GoChunker
		// does; each is named by its last segment, aliases ignored, and the
		// wildcard names nothing.
		"Config": {"imports:HashMap", "imports:load", "imports:save", "imports:store"},
		"Repo":   {},
		"get":    {"calls:helper", "calls:self.fetch"},
		// self.fetch() inside fetch is recursion, so no edge.
		"fetch": {},
		// A use inside a function belongs to it; helper() is recursion.
		"helper": {"imports:Write"},
		// An inline mod is a ChunkFile chunk: it owns nothing, neither the
		// call nor the use in its body.
		"inner": {},
	}
	for name, edges := range want {
		if strings.Join(got[name], ",") != strings.Join(edges, ",") {
			t.Errorf("%s edges = %v, want %v", name, got[name], edges)
		}
	}
	for name, edges := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected chunk %q with edges %v", name, edges)
		}
		for _, e := range edges {
			if strings.HasPrefix(e, string(semantic.RefUsesType)+":") {
				t.Errorf("%s has a uses_type edge %q; a Rust path or field access is not a type use", name, e)
			}
		}
	}
}

// rustRefStrings extracts refs with a bare RustChunker and renders each as
// "chunk type:name".
func rustRefStrings(t *testing.T, src string) []string {
	t.Helper()
	c := NewRustChunker()
	chunks, err := c.Chunk("m.rs", []byte(src))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	refs, err := c.ExtractRefs("m.rs", []byte(src), chunks)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	names := map[string]string{}
	for _, ch := range chunks {
		names[ch.ID] = ch.Name
	}
	var got []string
	for _, r := range refs {
		got = append(got, names[r.ChunkID]+" "+string(r.RefType)+":"+r.RefName)
	}
	return got
}

// Defends recursion being one rule, as in GoChunker and PythonChunker: f()
// inside f and self.f() inside method f write no self-edge, while a call to a
// differently named function or method keeps its edge.
func TestRustRefs_RecursionIsOneRuleForBareAndSelf(t *testing.T) {
	cases := map[string]string{
		"bare":      "fn f() {\n    f();\n}\n",
		"self":      "impl C {\n    fn f(&self) {\n        self.f();\n    }\n}\n",
		"other":     "impl C {\n    fn f(&self) {\n        self.g();\n    }\n}\n",
		"bareOther": "fn f() {\n    g();\n}\n",
		"generic":   "fn f() {\n    g::<u8>();\n}\n",
		"receiver":  "fn f(o: O) {\n    o.g();\n    (self.g)();\n}\n",
	}
	want := map[string]string{
		"bare": "", "self": "",
		"other": "f calls:self.g", "bareOther": "f calls:g", "generic": "f calls:g",
		// Only a self. receiver names a resolvable method.
		"receiver": "",
	}
	for name, src := range cases {
		if got := strings.Join(rustRefStrings(t, src), ","); got != want[name] {
			t.Errorf("%s: refs = %q, want %q", name, got, want[name])
		}
	}
}

// Defends use names matching PythonChunker.importNames and jsImportName: the
// last path segment, aliases ignored, nested {a, b} lists expanded, a self
// item named by its path, and nothing for a wildcard.
func TestRustRefs_UseNames(t *testing.T) {
	src := "use a::{b::{c, d as e}, f::*, self};\nuse g;\nuse ::h::i;\nuse {j, k};\nuse super::*;\n\nfn first() {}\n"
	want := "first imports:c,first imports:d,first imports:a,first imports:g,first imports:i,first imports:j,first imports:k"
	if got := strings.Join(rustRefStrings(t, src), ","); got != want {
		t.Errorf("refs = %q, want %q", got, want)
	}
}

// Defends "nothing to attribute to": a file with a use and a call but no
// chunk yields no edge and no error rather than an edge with an empty chunk
// ID; so does a file whose only chunk is an inline mod.
func TestRustRefs_NoOwnerYieldsNoEdge(t *testing.T) {
	c := NewRustChunker()
	refs, err := c.ExtractRefs("s.rs", []byte("use a::b;\nstatic X: u32 = f();\n"), nil)
	if err != nil || len(refs) != 0 {
		t.Errorf("no chunks: refs = %v, err = %v; want none", refs, err)
	}
	if got := rustRefStrings(t, "mod m {\n    use a::b;\n    fn g() {\n        h();\n    }\n}\n"); len(got) != 0 {
		t.Errorf("mod only: refs = %v, want none", got)
	}
}

// Defends "a wrong edge is worse than none" for code that does not parse: a
// call or use the grammar still found inside a fallback text window must not
// become an edge, from the window or from any other chunk, while the clean
// function keeps its own.
func TestRustRefs_NoEdgeFromAFallbackTextWindow(t *testing.T) {
	src := []byte("fn ok() {\n    good();\n}\n\nuse a::b;\nstatic X: u32 = bad(1) +;\n")
	f := WithFallback(NewRustChunker())
	chunks, err := f.Chunk("b.rs", src)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	windows := 0
	for _, c := range chunks {
		if c.Type == semantic.ChunkFile {
			windows++
		}
	}
	if windows == 0 {
		t.Fatalf("precondition: no text window in %+v; the fixture no longer falls back", chunks)
	}
	refs, err := f.ExtractRefs("b.rs", src, chunks)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	if len(refs) != 1 || refs[0].RefName != "good" {
		t.Errorf("refs = %+v, want only ok's call to good", refs)
	}
}
