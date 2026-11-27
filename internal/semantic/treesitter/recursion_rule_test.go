package treesitter

import (
	"sort"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/v2/internal/semantic"
)

// recursionCallRows extracts the calls edges of src through the extractor the
// indexer would use for path, and renders each as "<chunk type> <chunk name>
// calls <callee>". The callee is cut to the text after its last dot, the part
// the resolver links on, so this.n, self.n and c.n read alike across languages.
func recursionCallRows(t *testing.T, path, src string) []string {
	t.Helper()
	factory := semantic.NewChunkerFactory()
	RegisterAll(factory)
	factory.Register("go", semantic.NewGoChunker())
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
	byID := map[string]semantic.Chunk{}
	for _, ch := range chunks {
		byID[ch.ID] = ch
	}
	var rows []string
	for _, r := range refs {
		if r.RefType != semantic.RefCalls {
			continue
		}
		ch := byID[r.ChunkID]
		rows = append(rows, ch.Type.String()+" "+ch.Name+" calls "+r.RefName[strings.LastIndex(r.RefName, ".")+1:])
	}
	sort.Strings(rows)
	return rows
}

// Defends one recursion rule across every language that writes call edges
// (epic 37 T1, owner answers EST-8928 Q1 and Q2). The same program must give
// the same `callers` answer whichever language it is written in:
//
//   - f() inside function f is recursion and writes no edge;
//   - recv.m() inside method m (this., self., cls., a Go receiver) is
//     recursion and writes no edge;
//   - a bare m() inside method m is not recursion: no language calls a method
//     by its bare name, so it names the free function m and keeps its edge.
//
// Before, JS/TS recorded "f calls f" and "m calls this.m"; Go recorded
// "m calls c.m"; and Go, Python and Rust dropped the bare m() a method makes to
// the free function, so `callers m` missed a real caller (row c6030299c054c41a).
func TestRecursionIsOneRuleAcrossLanguages(t *testing.T) {
	programs := []struct {
		name string
		want []string
		src  map[string]string
	}{
		{
			name: "function calls itself",
			want: []string{"function f calls g"},
			src: map[string]string{
				"a.go": "package a\n\nfunc f() {\n\tf()\n\tg()\n}\n\nfunc g() {}\n",
				"a.py": "def f():\n    f()\n    g()\n\ndef g():\n    pass\n",
				"a.rs": "fn f() {\n    f();\n    g();\n}\n\nfn g() {}\n",
				"a.js": "function f() {\n  f();\n  g();\n}\n\nfunction g() {}\n",
				"a.ts": "function f(): void {\n  f();\n  g();\n}\n\nfunction g(): void {}\n",
			},
		},
		{
			name: "method calls itself through its receiver",
			want: []string{"method m calls n"},
			src: map[string]string{
				"a.go": "package a\n\ntype C struct{}\n\nfunc (c *C) m() {\n\tc.m()\n\tc.n()\n}\n\nfunc (c *C) n() {}\n",
				"a.py": "class C:\n    def m(self):\n        self.m()\n        self.n()\n\n    def n(self):\n        pass\n",
				"a.rs": "impl C {\n    fn m(&self) {\n        self.m();\n        self.n();\n    }\n\n    fn n(&self) {}\n}\n",
				"a.js": "class C {\n  m() {\n    this.m();\n    this.n();\n  }\n\n  n() {}\n}\n",
				"a.ts": "class C {\n  m(): void {\n    this.m();\n    this.n();\n  }\n\n  n(): void {}\n}\n",
			},
		},
		{
			name: "method calls the free function of its own name",
			want: []string{"method m calls m"},
			src: map[string]string{
				"a.go": "package a\n\nfunc m() {}\n\ntype C struct{}\n\nfunc (c *C) m() {\n\tm()\n}\n",
				"a.py": "def m():\n    pass\n\nclass C:\n    def m(self):\n        m()\n",
				"a.rs": "fn m() {}\n\nimpl C {\n    fn m(&self) {\n        m();\n    }\n}\n",
				"a.js": "function m() {}\n\nclass C {\n  m() {\n    m();\n  }\n}\n",
				"a.ts": "function m(): void {}\n\nclass C {\n  m(): void {\n    m();\n  }\n}\n",
			},
		},
	}
	for _, p := range programs {
		for _, path := range []string{"a.go", "a.py", "a.rs", "a.js", "a.ts"} {
			t.Run(p.name+"/"+path, func(t *testing.T) {
				got := recursionCallRows(t, path, p.src[path])
				if strings.Join(got, "\n") != strings.Join(p.want, "\n") {
					t.Errorf("calls edges:\n  got  %q\n  want %q", got, p.want)
				}
			})
		}
	}
}

// Defends Q2's own case (row c6030299c054c41a): a bare f() inside Python
// method f calls the module-level f and keeps its edge, while self.f() and
// cls.f() inside f stay dropped as recursion.
func TestPythonRefs_BareCallInsideSameNamedMethodKeepsItsEdge(t *testing.T) {
	src := "def f():\n    pass\n\nclass C:\n    def f(self):\n        f()\n        self.f()\n\n    @classmethod\n    def g(cls):\n        cls.g()\n"
	if got, want := strings.Join(pythonRefStrings(t, src), ","), "f calls:f"; got != want {
		t.Errorf("refs = %q, want %q", got, want)
	}
}
