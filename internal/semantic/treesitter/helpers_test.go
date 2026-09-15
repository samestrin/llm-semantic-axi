package treesitter

import (
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter"
	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// findNamedDescendant returns the first node of the given type reachable from
// root through named children, so a test can hand a specific AST node to a
// helper that the chunkers only ever call from deep inside a walk.
func findNamedDescendant(root *gotreesitter.Node, lang *gotreesitter.Language, want string) *gotreesitter.Node {
	var found *gotreesitter.Node
	var walk func(n *gotreesitter.Node)
	walk = func(n *gotreesitter.Node) {
		WalkNamedChildren(n, lang, func(child *gotreesitter.Node, nodeType string) {
			if found != nil {
				return
			}
			if nodeType == want {
				found = child
				return
			}
			walk(child)
		})
	}
	walk(root)
	return found
}

// --- IncludesDecorators ---

func TestIncludesDecorators_ReturnsTheDecoratedParent(t *testing.T) {
	// A decorated Python def is two nodes: the decorated_definition wrapper and
	// the function_definition inside it. Chunking the inner node alone would
	// drop `@staticmethod` / `@app.route(...)` from the stored content, which is
	// often the only thing that says what the function is for.
	c := NewPythonChunker()
	src := []byte("@app.route(\"/users\")\n@staticmethod\ndef handler(req):\n    return req\n")
	tree, err := ParseSource(src, c.lang)
	if err != nil {
		t.Fatalf("ParseSource: %v", err)
	}
	defer tree.Release()

	fn := findNamedDescendant(tree.RootNode(), c.lang, "function_definition")
	if fn == nil {
		t.Fatal("no function_definition in the parsed source")
	}

	startNode, decoratorText := IncludesDecorators(fn, c.lang, src)
	if got := startNode.Type(c.lang); got != "decorated_definition" {
		t.Errorf("startNode type = %q, want decorated_definition", got)
	}
	if startNode.StartByte() != 0 {
		t.Errorf("startNode starts at byte %d, want 0 — the decorators were left out", startNode.StartByte())
	}
	if !strings.Contains(decoratorText, "@app.route(\"/users\")") ||
		!strings.Contains(decoratorText, "@staticmethod") {
		t.Errorf("decoratorText = %q, want both decorators", decoratorText)
	}
	if strings.Contains(decoratorText, "def handler") {
		t.Errorf("decoratorText leaked the definition itself: %q", decoratorText)
	}
}

func TestIncludesDecorators_LeavesAnUndecoratedNodeAlone(t *testing.T) {
	// The no-decorator case must be a pass-through. Returning the parent here
	// would widen every plain function to its enclosing module or class body.
	c := NewPythonChunker()
	src := []byte("def plain(x):\n    return x\n")
	tree, err := ParseSource(src, c.lang)
	if err != nil {
		t.Fatalf("ParseSource: %v", err)
	}
	defer tree.Release()

	fn := findNamedDescendant(tree.RootNode(), c.lang, "function_definition")
	if fn == nil {
		t.Fatal("no function_definition in the parsed source")
	}

	startNode, decoratorText := IncludesDecorators(fn, c.lang, src)
	if startNode != fn {
		t.Errorf("startNode = %v, want the original node back", startNode.Type(c.lang))
	}
	if decoratorText != "" {
		t.Errorf("decoratorText = %q, want empty", decoratorText)
	}
}

// --- Rust gaps ---

func TestRustChunker_Extensions(t *testing.T) {
	// Rust claims exactly one extension; anything else here would make the
	// dispatcher hand .rlib or .toml files to a Rust parser.
	got := NewRustChunker().SupportedExtensions()
	if len(got) != 1 || got[0] != "rs" {
		t.Errorf("SupportedExtensions() = %v, want [rs]", got)
	}
}

func TestRustChunker_InlineModIsChunkedAndDeclarationIsNot(t *testing.T) {
	// `mod foo;` is a pointer at another file that this chunker will reach on
	// its own pass — emitting it would store a chunk whose content is one line
	// of nothing. Only a module with a body carries indexable code.
	src := []byte(`mod declared_elsewhere;

mod inline {
    pub fn helper() -> u8 {
        1
    }
}
`)
	chunks, err := NewRustChunker().Chunk("lib.rs", []byte(src))
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	byName := chunksByName(chunks)

	if _, ok := byName["declared_elsewhere"]; ok {
		t.Error("a bodyless `mod x;` declaration was emitted as a chunk")
	}

	mod, ok := byName["inline"]
	if !ok {
		t.Fatalf("inline module missing from %v", chunks)
	}
	if mod.Type != semantic.ChunkFile {
		t.Errorf("inline module type = %v, want ChunkFile", mod.Type)
	}
	if mod.Signature != "mod inline" {
		t.Errorf("inline module signature = %q, want %q", mod.Signature, "mod inline")
	}
	if !strings.Contains(mod.Content, "pub fn helper") {
		t.Errorf("module chunk content lost its body:\n%s", mod.Content)
	}
}

// --- JavaScript/TypeScript gaps ---

func TestJSChunker_PlainJavaScriptUsesTheJSGrammar(t *testing.T) {
	// langForExt falls through to the JS grammar for everything that is not
	// .ts/.tsx. A .js file carrying no TypeScript syntax must still chunk, and
	// the chunks must be tagged `js` rather than inheriting `ts`.
	src := []byte(`export function createApp() {
    return {};
}

const handleError = (err) => {
    console.error(err);
};

class Widget {
    render() {
        return null;
    }
}
`)
	chunks, err := NewJSChunker().Chunk("app.js", src)
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	byName := chunksByName(chunks)

	for _, name := range []string{"createApp", "handleError", "Widget", "render"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("expected to find %q in %v", name, chunks)
		}
	}
	for _, c := range chunks {
		if c.Language != "js" {
			t.Errorf("chunk %q has language %q, want 'js'", c.Name, c.Language)
		}
	}
}

func TestJSChunker_ArrowFunctionTypeFollowsTheEnclosingScope(t *testing.T) {
	// An arrow function assigned at file scope is a function; the same shape
	// reached with an enclosing class name is a method. This is the one place
	// extractArrowFunctions branches on scope.
	src := []byte("const at_file_scope = () => 1;\n")
	c := NewJSChunker()
	tree, err := ParseSource(src, c.jsLang)
	if err != nil {
		t.Fatalf("ParseSource: %v", err)
	}
	defer tree.Release()

	decl := findNamedDescendant(tree.RootNode(), c.jsLang, "lexical_declaration")
	if decl == nil {
		t.Fatal("no lexical_declaration in the parsed source")
	}

	var scoped []semantic.Chunk
	c.extractArrowFunctions(decl, c.jsLang, src, "a.js", "js", &scoped, "Widget")
	if len(scoped) != 1 {
		t.Fatalf("got %d chunks, want 1", len(scoped))
	}
	if scoped[0].Type != semantic.ChunkMethod {
		t.Errorf("arrow inside a class scope has type %v, want ChunkMethod", scoped[0].Type)
	}

	var unscoped []semantic.Chunk
	c.extractArrowFunctions(decl, c.jsLang, src, "a.js", "js", &unscoped, "")
	if len(unscoped) != 1 {
		t.Fatalf("got %d chunks, want 1", len(unscoped))
	}
	if unscoped[0].Type != semantic.ChunkFunction {
		t.Errorf("arrow at file scope has type %v, want ChunkFunction", unscoped[0].Type)
	}
}

func TestJSChunker_NonFunctionConstIsNotAChunk(t *testing.T) {
	// extractArrowFunctions is reached for every `const`/`let`. Only the ones
	// whose value is a function may become chunks, or an index of a config file
	// fills up with string literals.
	src := []byte("const PORT = 8080;\nconst NAMES = ['a', 'b'];\n")
	chunks, err := NewJSChunker().Chunk("config.js", src)
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("got %d chunks for value-only declarations, want 0: %v", len(chunks), chunks)
	}
}

// --- Shared helper gaps ---

func TestExtractSignature_SingleLineNodeDropsTheTrailingBrace(t *testing.T) {
	// ExtractSignature is called on nodes that may be one line long. Both the
	// multi-line and the single-line path must strip the trailing " {" so the
	// two shapes produce the same signature text for the same declaration.
	c := NewJSChunker()
	src := []byte("function oneLiner(a) { return a; }\n")
	tree, err := ParseSource(src, c.jsLang)
	if err != nil {
		t.Fatalf("ParseSource: %v", err)
	}
	defer tree.Release()

	fn := findNamedDescendant(tree.RootNode(), c.jsLang, "function_declaration")
	if fn == nil {
		t.Fatal("no function_declaration in the parsed source")
	}
	if got := ExtractSignature(fn, src); got != "function oneLiner(a) { return a; }" {
		t.Errorf("ExtractSignature = %q", got)
	}
}

func TestTruncateContent_SplitsAtTheLastNewlineAndMarksTheCut(t *testing.T) {
	// A truncated chunk must not end mid-token, and must say it was truncated —
	// otherwise a reader treats a half-function as the whole function.
	long := "aaaa\nbbbb\ncccc\ndddd\neeee\n"
	got := TruncateContent(long, 12)
	if !strings.HasSuffix(got, "\n// ... truncated") {
		t.Errorf("no truncation marker: %q", got)
	}
	if !strings.HasPrefix(got, "aaaa\nbbbb") {
		t.Errorf("content before the cut was lost: %q", got)
	}
	if strings.Contains(got, "eeee") {
		t.Errorf("content past the limit survived: %q", got)
	}

	// When no newline sits past the halfway mark there is nothing to split on,
	// so the cut lands exactly at maxSize.
	noBreak := strings.Repeat("x", 50)
	got = TruncateContent(noBreak, 10)
	if got != strings.Repeat("x", 10)+"\n// ... truncated" {
		t.Errorf("hard cut = %q", got)
	}

	// Exactly at the limit is not over it.
	if got := TruncateContent("12345", 5); got != "12345" {
		t.Errorf("content at exactly maxSize was truncated: %q", got)
	}
}

func TestGetNodeText_NilNodeIsEmpty(t *testing.T) {
	// Every extractor calls GetNodeText on the result of a field lookup that can
	// miss. Returning "" rather than panicking is what turns a malformed
	// declaration into a skipped chunk instead of a crashed index run.
	if got := GetNodeText(nil, []byte("anything")); got != "" {
		t.Errorf("GetNodeText(nil) = %q, want empty", got)
	}
}

func TestExtractors_NeverEmitANamelessChunk(t *testing.T) {
	// Every extractor opens with the same guard: look up the declaration's name
	// field, and bail if it is absent. Tree-sitter hands back exactly that shape
	// when it error-recovers a half-written declaration, and a chunk with an
	// empty Name is unsearchable — it can never be matched by name, and it
	// collides with every other nameless chunk when an ID is generated from it.
	//
	// Each case below feeds an extractor a real node that carries no `name`
	// (`type` for impl) field, and asserts the guard drops it silently rather
	// than appending.
	rootOf := func(t *testing.T, src string, lang *gotreesitter.Language) (*gotreesitter.Node, []byte, func()) {
		t.Helper()
		content := []byte(src)
		tree, err := ParseSource(content, lang)
		if err != nil {
			t.Fatalf("ParseSource: %v", err)
		}
		return tree.RootNode(), content, tree.Release
	}

	py := NewPythonChunker()
	pyRoot, pySrc, pyDone := rootOf(t, "x = 1\n", py.lang)
	defer pyDone()

	js := NewJSChunker()
	jsRoot, jsSrc, jsDone := rootOf(t, "const x = 1;\n", js.tsLang)
	defer jsDone()

	rs := NewRustChunker()
	rsRoot, rsSrc, rsDone := rootOf(t, "const X: u8 = 1;\n", rs.lang)
	defer rsDone()

	php := NewPHPChunker()
	phpRoot, phpSrc, phpDone := rootOf(t, "<?php\n$x = 1;\n", php.lang)
	defer phpDone()

	cases := map[string]func(chunks *[]semantic.Chunk){
		"python function":   func(c *[]semantic.Chunk) { py.extractFunction(pyRoot, pySrc, "a.py", c, "") },
		"python class":      func(c *[]semantic.Chunk) { py.extractClass(pyRoot, pySrc, "a.py", c) },
		"js function":       func(c *[]semantic.Chunk) { js.extractFunction(jsRoot, js.tsLang, jsSrc, "a.ts", "ts", c, "") },
		"js class":          func(c *[]semantic.Chunk) { js.extractClass(jsRoot, js.tsLang, jsSrc, "a.ts", "ts", c) },
		"js method":         func(c *[]semantic.Chunk) { js.extractMethod(jsRoot, js.tsLang, jsSrc, "a.ts", "ts", c, "K") },
		"js interface":      func(c *[]semantic.Chunk) { js.extractInterface(jsRoot, js.tsLang, jsSrc, "a.ts", "ts", c) },
		"js type alias":     func(c *[]semantic.Chunk) { js.extractTypeAlias(jsRoot, js.tsLang, jsSrc, "a.ts", "ts", c) },
		"rust function":     func(c *[]semantic.Chunk) { rs.extractFunction(rsRoot, rsSrc, "a.rs", c, "") },
		"rust struct":       func(c *[]semantic.Chunk) { rs.extractStruct(rsRoot, rsSrc, "a.rs", c) },
		"rust enum":         func(c *[]semantic.Chunk) { rs.extractEnum(rsRoot, rsSrc, "a.rs", c) },
		"rust trait":        func(c *[]semantic.Chunk) { rs.extractTrait(rsRoot, rsSrc, "a.rs", c) },
		"rust impl":         func(c *[]semantic.Chunk) { rs.extractImpl(rsRoot, rsSrc, "a.rs", c) },
		"rust mod":          func(c *[]semantic.Chunk) { rs.extractMod(rsRoot, rsSrc, "a.rs", c) },
		"rust type alias":   func(c *[]semantic.Chunk) { rs.extractTypeAlias(rsRoot, rsSrc, "a.rs", c) },
		"php function":      func(c *[]semantic.Chunk) { php.extractFunction(phpRoot, phpSrc, "a.php", c, "") },
		"php class":         func(c *[]semantic.Chunk) { php.extractClass(phpRoot, phpSrc, "a.php", c) },
		"php interface":     func(c *[]semantic.Chunk) { php.extractInterface(phpRoot, phpSrc, "a.php", c) },
		"php trait":         func(c *[]semantic.Chunk) { php.extractTrait(phpRoot, phpSrc, "a.php", c) },
		"php method (self)": func(c *[]semantic.Chunk) { php.extractFunction(phpRoot, phpSrc, "a.php", c, "Cls") },
	}

	for name, extract := range cases {
		t.Run(name, func(t *testing.T) {
			var chunks []semantic.Chunk
			extract(&chunks)
			if len(chunks) != 0 {
				t.Errorf("emitted %d chunk(s) for a node with no name: %+v", len(chunks), chunks)
			}
		})
	}
}

func TestGetLangEntry_ResolvesEveryChunkedLanguage(t *testing.T) {
	// GetLangEntry builds a filename from a bare extension. If that mapping
	// broke, the dispatcher would fall back to line chunking for a language this
	// package has a real parser for.
	for _, ext := range []string{"py", "js", "ts", "rs", "php"} {
		if GetLangEntry(ext) == nil {
			t.Errorf("GetLangEntry(%q) = nil, want a grammar entry", ext)
		}
	}
}
