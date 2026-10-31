package treesitter

import (
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// ktChunk is the part of a chunk the line-range tests compare.
type ktChunk struct {
	Name       string
	Type       semantic.ChunkType
	Start, End int
}

func ktChunks(chunks []semantic.Chunk) []string {
	out := make([]string, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, ktChunk{c.Name, c.Type, c.StartLine, c.EndLine}.key())
	}
	sort.Strings(out)
	return out
}

func (c ktChunk) key() string {
	return c.Name + " " + c.Type.String() + " " + strconv.Itoa(c.Start) + "-" + strconv.Itoa(c.End)
}

func assertKotlinChunks(t *testing.T, chunks []semantic.Chunk, want []ktChunk) {
	t.Helper()
	wantKeys := make([]string, 0, len(want))
	for _, w := range want {
		wantKeys = append(wantKeys, w.key())
	}
	sort.Strings(wantKeys)
	got := ktChunks(chunks)
	if strings.Join(got, "\n") != strings.Join(wantKeys, "\n") {
		t.Errorf("chunks differ\n got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(wantKeys, "\n  "))
	}
}

// ktSampleChunks is every chunk testdata/kotlin/sample.kt must produce, with
// the lines it spans in that file.
var ktSampleChunks = []ktChunk{
	{"Shape", semantic.ChunkInterface, 5, 7},
	{"area", semantic.ChunkMethod, 6, 6},
	{"Circle", semantic.ChunkStruct, 9, 24},
	{"Circle", semantic.ChunkMethod, 11, 11},
	{"area", semantic.ChunkMethod, 13, 15},
	{"Factory", semantic.ChunkStruct, 17, 19},
	{"unit", semantic.ChunkMethod, 18, 18},
	{"Inner", semantic.ChunkStruct, 21, 23},
	{"run", semantic.ChunkMethod, 22, 22},
	{"Registry", semantic.ChunkStruct, 26, 30},
	{"register", semantic.ChunkMethod, 27, 29},
	{"Color", semantic.ChunkStruct, 32, 36},
	{"lower", semantic.ChunkMethod, 35, 35},
	{"Point", semantic.ChunkStruct, 38, 38},
	{"Action", semantic.ChunkInterface, 40, 42},
	{"invoke", semantic.ChunkMethod, 41, 41},
	{"topLevel", semantic.ChunkFunction, 44, 46},
	{"shout", semantic.ChunkFunction, 48, 48},
	{"Expr", semantic.ChunkStruct, 50, 56},
	{"Zero", semantic.ChunkStruct, 51, 51},
	{"Companion", semantic.ChunkStruct, 52, 55},
	{"zero", semantic.ChunkMethod, 53, 54},
}

func TestKotlinChunker_Extensions(t *testing.T) {
	// The extensions are the dispatcher's registry keys: a .kts script must
	// reach this chunker as well as a .kt file, and nothing else may.
	got := NewKotlinChunker().SupportedExtensions()
	if strings.Join(got, ",") != "kt,kts" {
		t.Errorf("SupportedExtensions() = %v, want [kt kts]", got)
	}
}

func TestKotlinChunker_SampleNamesTypesAndLineRanges(t *testing.T) {
	// Defends the task's "done when": classes, objects, interfaces and
	// functions each come back with the right name, type and 1-based line
	// range, and nothing else is emitted. An off-by-one here makes every search
	// hit open the wrong lines.
	chunks, err := NewKotlinChunker().Chunk("sample.kt", readTestFile(t, "kotlin", "sample.kt"))
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	assertKotlinChunks(t, chunks, ktSampleChunks)
}

func TestKotlinChunker_ScriptFile(t *testing.T) {
	// A .kts script mixes declarations with top-level statements. The
	// declarations must still be chunked; the statements are not declarations
	// and must not become nameless chunks.
	chunks, err := NewKotlinChunker().Chunk("script.kts", readTestFile(t, "kotlin", "script.kts"))
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	assertKotlinChunks(t, chunks, []ktChunk{
		{"greet", semantic.ChunkFunction, 5, 7},
		{"Helper", semantic.ChunkStruct, 9, 11},
		{"help", semantic.ChunkMethod, 10, 10},
		{"Config", semantic.ChunkStruct, 13, 15},
	})
}

func TestKotlinChunker_TopLevelObjectWithoutModifiers(t *testing.T) {
	// The bundled grammar parses a bare top-level `object Name { ... }` as an
	// infix expression (`object` `Name` `{ lambda }`), not as an
	// object_declaration. Without handling that shape the most common Kotlin
	// singleton silently vanishes from the index while the file reports as
	// parsed. A modifier (`private object`) parses normally; both must chunk.
	src := []byte("object Bare {\n    fun a() {}\n}\n\nprivate object Hidden {\n    fun b() {}\n}\n")
	chunks, err := NewKotlinChunker().Chunk("o.kt", src)
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	assertKotlinChunks(t, chunks, []ktChunk{
		{"Bare", semantic.ChunkStruct, 1, 3},
		{"a", semantic.ChunkMethod, 2, 2},
		{"Hidden", semantic.ChunkStruct, 5, 7},
		{"b", semantic.ChunkMethod, 6, 6},
	})
}

func TestKotlinChunker_InfixExpressionThatIsNotAnObjectIsIgnored(t *testing.T) {
	// The object workaround keys on the exact `object Name {}` shape. Each
	// line below is an infix expression that is not that shape, and each would
	// become a bogus chunk named after its operator or operand if one guard in
	// extractBareObject were dropped:
	//   - `a to b`, `x shl 3`: the left operand is not `object`.
	//   - `object {} to {}`: an anonymous object (its `object` node owns a class
	//     body) with a lambda on the right; without the keyword-only check this
	//     was chunked as a struct named "to".
	//   - `object {} to 1`, `object Foo bar {}`: the right operand is not a
	//     lambda body.
	//   - an anonymous object followed by `apply { }`.
	for _, src := range []string{
		"val p = 1 to 2\nx shl 3\n",
		"object {} to {}\n",
		"object {} to 1\n",
		"object Foo bar {}\n",
		"object : Runnable { override fun run() {} } apply {\n}\n",
	} {
		chunks, err := NewKotlinChunker().Chunk("s.kts", []byte(src))
		if err != nil {
			t.Fatalf("Chunk(%q) failed: %v", src, err)
		}
		if len(chunks) != 0 {
			t.Errorf("Chunk(%q) = %d chunks, want 0: %v", src, len(chunks), ktChunks(chunks))
		}
	}
}

func TestKotlinChunker_BareObjectWithACommentInItsHeader(t *testing.T) {
	// A comment is a named node, so `object /* c */ Name {}` gives the infix
	// expression four named children instead of three. Counting the comment
	// dropped the object from the index while the file reported as parsed.
	src := []byte("object /* registry */ Registry {\n    fun a() {}\n}\n\nobject Other /* c */ {\n}\n")
	chunks, err := NewKotlinChunker().Chunk("o.kt", src)
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	assertKotlinChunks(t, chunks, []ktChunk{
		{"Registry", semantic.ChunkStruct, 1, 3},
		{"a", semantic.ChunkMethod, 2, 2},
		{"Other", semantic.ChunkStruct, 5, 6},
	})
}

func TestKotlinChunker_DetachedAnnotationStaysWithItsDeclaration(t *testing.T) {
	// At file scope the grammar splits `@Deprecated("x")` off the declaration
	// below it (an annotation node then a parenthesized expression). The chunk
	// must still start at the annotation, or its line belongs to no chunk and
	// the stored content loses what the class is marked as.
	src := []byte("@Suppress(\"a\")\n@Deprecated(\"b\")\nclass A {\n}\n\n@JvmName(\"c\")\nfun f() {}\n")
	chunks, err := NewKotlinChunker().Chunk("a.kt", src)
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	assertKotlinChunks(t, chunks, []ktChunk{
		{"A", semantic.ChunkStruct, 1, 4},
		{"f", semantic.ChunkFunction, 6, 7},
	})
	byName := chunksByName(chunks)
	if !strings.HasPrefix(byName["A"].Content, "@Suppress(\"a\")\n@Deprecated(\"b\")\nclass A") {
		t.Errorf("A content lost its annotations: %q", byName["A"].Content)
	}
	if byName["A"].Signature != "class A" {
		t.Errorf("A signature = %q, want %q", byName["A"].Signature, "class A")
	}
	if byName["f"].Signature != "fun f() {}" {
		t.Errorf("f signature = %q", byName["f"].Signature)
	}
}

func TestKotlinChunker_AnnotationIsNotTheSignature(t *testing.T) {
	// An annotation inside the modifiers starts the declaration's first line.
	// The signature is what results display; "@JvmStatic" alone would hide the
	// function, and "@Deprecated(...)" would hide the class.
	chunks, err := NewKotlinChunker().Chunk("sample.kt", readTestFile(t, "kotlin", "sample.kt"))
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	sigs := map[string]string{}
	for _, c := range chunks {
		sigs[c.Name+"/"+strconv.Itoa(c.StartLine)] = c.Signature
	}
	want := map[string]string{
		"zero/53":      "fun zero(): Expr = Zero",
		"Circle/9":     "class Circle(private val r: Double) : Shape",
		"Circle/11":    "constructor() : this(1.0)",
		"area/13":      "override fun area(): Double",
		"Companion/52": "companion object",
		"shout/48":     "fun String.shout(): String = uppercase()",
	}
	for k, w := range want {
		if sigs[k] != w {
			t.Errorf("signature %s = %q, want %q", k, sigs[k], w)
		}
	}
	for _, c := range chunks {
		if c.Name == "zero" && !strings.HasPrefix(c.Content, "@JvmStatic") {
			t.Errorf("zero content lost its annotation: %q", c.Content)
		}
	}
}

func TestKotlinChunker_LocalDeclarationsAreNotChunked(t *testing.T) {
	// A function body is not a scope the index chunks: a local fun or an
	// anonymous object inside main is part of main's chunk, not its own.
	src := []byte("fun main() {\n    val o = object : Runnable {\n        override fun run() {}\n    }\n    fun local() {}\n}\n")
	chunks, err := NewKotlinChunker().Chunk("m.kt", src)
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	assertKotlinChunks(t, chunks, []ktChunk{{"main", semantic.ChunkFunction, 1, 6}})
}

func TestKotlinChunker_ChunkFields(t *testing.T) {
	// Every chunk must be storable: ID, language tag, path, valid line range.
	chunks, err := NewKotlinChunker().Chunk("sample.kt", readTestFile(t, "kotlin", "sample.kt"))
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	ids := map[string]bool{}
	for _, c := range chunks {
		if c.ID == "" || c.Language != "kotlin" || c.FilePath != "sample.kt" ||
			c.StartLine <= 0 || c.EndLine < c.StartLine || c.Content == "" || c.Signature == "" {
			t.Errorf("bad chunk: %+v", c)
		}
		if ids[c.ID] {
			t.Errorf("duplicate chunk ID %s for %q", c.ID, c.Name)
		}
		ids[c.ID] = true
	}
}

func TestKotlinChunker_EmptySourceYieldsNoChunks(t *testing.T) {
	// Empty or declaration-free files are normal input: no error, no chunks.
	for _, src := range []string{"", "package a\n", "val x = 1\n"} {
		chunks, err := NewKotlinChunker().Chunk("E.kt", []byte(src))
		if err != nil || len(chunks) != 0 {
			t.Errorf("Chunk(%q) = %d chunks, err %v; want 0, nil", src, len(chunks), err)
		}
	}
}

func TestKotlinChunker_SyntaxErrorIsReported(t *testing.T) {
	// A broken file must return ErrParseTree so the fallback wrapper can window
	// it instead of the walk silently passing a partial file off as whole.
	_, err := NewKotlinChunker().Chunk("B.kt", []byte("class A { fun f( { \n"))
	if err == nil {
		t.Error("expected ErrParseTree for broken source")
	}
}

func TestKotlinChunker_NamelessDeclarationIsDropped(t *testing.T) {
	// The name guard: a node without an identifier must not become a chunk.
	c := NewKotlinChunker()
	src := []byte("package a\n")
	tree, err := ParseSource(src, c.lang)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Release()
	var out []semantic.Chunk
	c.extractType(tree.RootNode(), tree.RootNode(), src, "a.kt", &out)
	c.extractFunction(tree.RootNode(), tree.RootNode(), src, "a.kt", &out, false)
	if len(out) != 0 {
		t.Errorf("emitted chunks for nameless nodes: %+v", out)
	}
}
