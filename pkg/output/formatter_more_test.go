package output

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests cover the formatter surfaces the original suite left open:
// map processing in minimal mode, cwd-relative paths, section headers, the
// emptiness classifiers across every kind, and the four PrintError modes.
// No t.Parallel anywhere in this package — New() reads package-level state.

// --- processMap ---

func TestMinimalJSON_AbbreviatesMapKeysWithoutDroppingZeroValues(t *testing.T) {
	// Struct fields opt into omission with `omitempty`; a map key has no tag to
	// opt in with, so a map's zero values are load-bearing data. A search result
	// carrying {"matches": 0} means "searched, found nothing" — dropping the key
	// turns that into "never ran".
	var buf bytes.Buffer
	f := New(true, true, &buf)

	data := map[string]interface{}{
		"file":    "/tmp/a.go",
		"matches": 0,
		"message": "",
		"custom":  "kept",
	}
	if err := f.Print(data, nil); err != nil {
		t.Fatalf("Print failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("Invalid JSON %q: %v", buf.String(), err)
	}

	if got, ok := parsed["f"]; !ok || got != "/tmp/a.go" {
		t.Errorf(`expected abbreviated key "f" = "/tmp/a.go", got %v`, parsed)
	}
	if got, ok := parsed["m"]; !ok {
		t.Errorf(`zero-valued map key "matches" was dropped: %v`, parsed)
	} else if got != float64(0) {
		t.Errorf(`"m" = %v, want 0`, got)
	}
	if _, ok := parsed["msg"]; !ok {
		t.Errorf(`empty-string map key "message" was dropped: %v`, parsed)
	}
	if got, ok := parsed["custom"]; !ok || got != "kept" {
		t.Errorf("an unabbreviated key must survive verbatim, got %v", parsed)
	}
	if _, ok := parsed["file"]; ok {
		t.Errorf("the full key name was emitted alongside the abbreviation: %v", parsed)
	}
}

func TestMinimalJSON_AbbreviatesNestedMapKeys(t *testing.T) {
	// processMap recurses through processForMinimalJSON, so the abbreviation
	// applies at every depth. A consumer reading `f` at the top level and `file`
	// one level down would need two key tables for one document.
	var buf bytes.Buffer
	f := New(true, true, &buf)

	data := map[string]interface{}{
		"result": map[string]interface{}{
			"file": "b.go",
			"line": 7,
		},
	}
	if err := f.Print(data, nil); err != nil {
		t.Fatalf("Print failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("Invalid JSON %q: %v", buf.String(), err)
	}
	inner, ok := parsed["r"].(map[string]interface{})
	if !ok {
		t.Fatalf(`expected abbreviated key "r" holding a map, got %v`, parsed)
	}
	if inner["f"] != "b.go" || inner["l"] != float64(7) {
		t.Errorf("nested map keys were not abbreviated: %v", inner)
	}
}

func TestMinimalJSON_NonStringMapKeysAreStringified(t *testing.T) {
	// JSON object keys are strings; a Go map keyed by int reaches processMap
	// with a non-string key and must be rendered rather than dropped or panicked
	// on.
	var buf bytes.Buffer
	f := New(true, true, &buf)

	if err := f.Print(map[int]string{7: "seven"}, nil); err != nil {
		t.Fatalf("Print failed: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("Invalid JSON %q: %v", buf.String(), err)
	}
	if parsed["7"] != "seven" {
		t.Errorf(`expected key "7" = "seven", got %v`, parsed)
	}
}

// --- processForMinimalJSON edges ---

func TestMinimalJSON_HandlesNilAndPointers(t *testing.T) {
	// The three shapes a handler can hand the formatter without meaning to:
	// an untyped nil, a nil pointer, and a pointer to a real struct. The first
	// two must render as null, and the third must be followed rather than
	// reflected on as a pointer.
	f := New(true, true, new(bytes.Buffer))

	if got := f.processForMinimalJSON(nil); got != nil {
		t.Errorf("processForMinimalJSON(nil) = %v, want nil", got)
	}

	var nilPtr *TestOutput
	if got := f.processForMinimalJSON(nilPtr); got != nil {
		t.Errorf("processForMinimalJSON((*TestOutput)(nil)) = %v, want nil", got)
	}

	got, ok := f.processForMinimalJSON(&TestOutput{Name: "via pointer"}).(map[string]interface{})
	if !ok {
		t.Fatalf("a pointer to a struct did not resolve to a map: %#v", got)
	}
	if got["n"] != "via pointer" {
		t.Errorf("pointer target was not processed: %v", got)
	}
}

func TestMinimalJSON_ScalarPassesThroughUnchanged(t *testing.T) {
	// A command that prints a bare string or number has nothing to abbreviate.
	// The default arm must return the value itself, not wrap it.
	f := New(true, true, new(bytes.Buffer))
	if got := f.processForMinimalJSON("plain"); got != "plain" {
		t.Errorf("processForMinimalJSON(%q) = %v", "plain", got)
	}
	if got := f.processForMinimalJSON(42); got != 42 {
		t.Errorf("processForMinimalJSON(42) = %v", got)
	}
}

// --- isEmpty / isEmptyValue across every kind ---

type everyKind struct {
	Str    string         `json:"str,omitempty"`
	Int    int64          `json:"int,omitempty"`
	Uint   uint32         `json:"uint,omitempty"`
	Float  float64        `json:"float,omitempty"`
	Bool   bool           `json:"bool,omitempty"`
	Slice  []string       `json:"slice,omitempty"`
	Map    map[string]int `json:"map,omitempty"`
	Ptr    *TestOutput    `json:"ptr,omitempty"`
	Iface  interface{}    `json:"iface,omitempty"`
	Struct TestOutput     `json:"struct,omitempty"`
	Ignore string         `json:"-"`
	NoTag  string
	Extra  map[string]interface{} `json:"extra,omitempty"`
}

func TestMinimalJSON_OmitsTheZeroOfEveryKind(t *testing.T) {
	// isEmpty is switched on reflect.Kind. A kind it does not recognise falls to
	// `return false` and the field is kept, so a missing arm shows up here as an
	// unexpected key rather than as a crash.
	var buf bytes.Buffer
	f := New(true, true, &buf)

	if err := f.Print(everyKind{Ignore: "dropped by tag", NoTag: "kept"}, nil); err != nil {
		t.Fatalf("Print failed: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("Invalid JSON %q: %v", buf.String(), err)
	}

	for _, key := range []string{"str", "int", "uint", "float", "bool", "slice", "map", "ptr", "iface", "extra"} {
		if _, ok := parsed[key]; ok {
			t.Errorf("zero-valued %q was not omitted: %v", key, parsed)
		}
	}
	// `json:"-"` is dropped outright, tag or no tag on emptiness.
	if _, ok := parsed["Ignore"]; ok {
		t.Errorf(`a json:"-" field was emitted: %v`, parsed)
	}
	// A field with no json tag keeps its Go name.
	if parsed["NoTag"] != "kept" {
		t.Errorf("untagged field lost: %v", parsed)
	}
	// A struct kind has no emptiness rule, so it survives even when zero.
	if _, ok := parsed["struct"]; !ok {
		t.Errorf("a zero struct field was omitted, but structs have no empty rule: %v", parsed)
	}
}

func TestMinimalJSON_KeepsTheNonZeroOfEveryKind(t *testing.T) {
	// The mirror image: every kind isEmpty knows about must survive when set.
	// An over-eager arm (say, treating false-but-present as absent) shows up
	// here as a missing key.
	var buf bytes.Buffer
	f := New(true, true, &buf)

	v := everyKind{
		Str:   "s",
		Int:   -1,
		Uint:  3,
		Float: 0.5,
		Bool:  true,
		Slice: []string{"a"},
		Map:   map[string]int{"k": 1},
		Ptr:   &TestOutput{Name: "p"},
		Iface: "i",
		Extra: map[string]interface{}{"count": 0},
	}
	if err := f.Print(v, nil); err != nil {
		t.Fatalf("Print failed: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("Invalid JSON %q: %v", buf.String(), err)
	}
	for _, key := range []string{"str", "int", "uint", "float", "bool", "slice", "map", "ptr", "iface", "extra"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("non-zero %q was omitted: %v", key, parsed)
		}
	}
	if ptr, ok := parsed["ptr"].(map[string]interface{}); !ok || ptr["n"] != "p" {
		t.Errorf("pointer field was not followed: %v", parsed["ptr"])
	}
}

func TestFilterEmpty_ClassifiesEveryKind(t *testing.T) {
	// FilterEmpty is the map-shaped twin of isEmpty and is applied by callers
	// that build their payload as a map. The two must agree about what "empty"
	// means, or the same value survives --min in one command and vanishes in
	// another.
	var nilPtr *TestOutput
	var nilIface interface{}

	empty := map[string]interface{}{
		"string": "",
		"int":    int64(0),
		"uint":   uint16(0),
		"float":  0.0,
		"bool":   false,
		"slice":  []string{},
		"map":    map[string]int{},
		"ptr":    nilPtr,
		"iface":  nilIface,
		"nil":    nil,
	}
	if got := FilterEmpty(empty); len(got) != 0 {
		t.Errorf("FilterEmpty kept %v, want nothing", got)
	}

	full := map[string]interface{}{
		"string": "x",
		"int":    int64(-2),
		"uint":   uint16(1),
		"float":  0.25,
		"bool":   true,
		"slice":  []string{"a"},
		"map":    map[string]int{"k": 1},
		"ptr":    &TestOutput{Name: "p"},
		"struct": TestOutput{},
	}
	got := FilterEmpty(full)
	if len(got) != len(full) {
		t.Errorf("FilterEmpty dropped values that were set: kept %d of %d (%v)", len(got), len(full), got)
	}
}

// --- RelativePath / RelativePathCwd ---

func TestRelativePathCwd_MakesPathsRelativeToTheWorkingDirectory(t *testing.T) {
	// Minimal mode prints paths relative to where the command was run, which is
	// the bulk of its token saving on a deep tree. The base is the process cwd,
	// not a flag, so this is the only thing that pins it.
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	want := filepath.Join("internal", "semantic", "chunker.go")
	if got := RelativePathCwd(filepath.Join(cwd, want)); got != want {
		t.Errorf("RelativePathCwd = %q, want %q", got, want)
	}

	// A path already inside cwd at the top level shortens to its base name.
	if got := RelativePathCwd(filepath.Join(cwd, "go.mod")); got != "go.mod" {
		t.Errorf("RelativePathCwd = %q, want %q", got, "go.mod")
	}
}

func TestRelativePath_KeepsAPathThatWouldClimbTooFar(t *testing.T) {
	// `../../../../x` is longer and less readable than the absolute path it
	// replaced, and it is meaningless to anyone who does not know the cwd. Past
	// three levels up the conversion is abandoned.
	abs := filepath.Join(string(filepath.Separator), "a", "b.go")
	base := filepath.Join(string(filepath.Separator), "x", "y", "z", "w", "v")
	if got := RelativePath(abs, base); got != abs {
		t.Errorf("RelativePath = %q, want the original %q", got, abs)
	}

	// Exactly two levels up is still shorter than absolute, so it is kept.
	base = filepath.Join(string(filepath.Separator), "a", "c", "d")
	if got := RelativePath(abs, base); !strings.HasPrefix(got, "..") {
		t.Errorf("RelativePath = %q, want a relative climb", got)
	}
}

// --- PrintSection ---

func TestPrintSection_WritesAHeaderOnlyOutsideMinimalMode(t *testing.T) {
	// Section rules are there for a human reading the terminal. In --min the
	// output is being parsed or fed to a model, where a `---` line is pure token
	// cost, so the header is suppressed entirely rather than shortened.
	var buf bytes.Buffer
	New(false, false, &buf).PrintSection("Results")
	if buf.String() != "---\nResults\n" {
		t.Errorf("PrintSection wrote %q, want %q", buf.String(), "---\nResults\n")
	}

	buf.Reset()
	New(false, true, &buf).PrintSection("Results")
	if buf.Len() != 0 {
		t.Errorf("PrintSection wrote %q in minimal mode, want nothing", buf.String())
	}
}

// --- New ---

func TestNew_DefaultsToStdoutWhenNoWriterIsGiven(t *testing.T) {
	// Handlers that construct a formatter without a writer are printing for the
	// user. Leaving Writer nil would panic on the first Fprintf.
	f := New(false, false, nil)
	if f.Writer != os.Stdout {
		t.Errorf("New(..., nil).Writer = %v, want os.Stdout", f.Writer)
	}
}

// --- printJSON error paths ---

type unmarshalable struct {
	Ch chan int `json:"ch"`
}

func TestPrintJSON_ReportsAValueItCannotMarshal(t *testing.T) {
	// A marshal failure must reach the caller as an error, in both JSON modes.
	// Swallowing it would exit 0 having printed nothing, which reads as "no
	// results" rather than "the command is broken".
	for name, minimal := range map[string]bool{"pretty": false, "minimal": true} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			f := New(true, minimal, &buf)
			if err := f.Print(unmarshalable{Ch: make(chan int)}, nil); err == nil {
				t.Fatal("an unmarshalable value was accepted")
			}
			if buf.Len() != 0 {
				t.Errorf("a partial payload was written before failing: %q", buf.String())
			}
		})
	}
}

// --- PrintError across the output modes ---

func TestPrintError_PrettyJSONCarriesTheFullKeyNames(t *testing.T) {
	// --json without --min is the mode a human or a jq pipeline reads, so the
	// keys stay spelled out and the document stays indented.
	var buf bytes.Buffer
	f := New(true, false, &buf)

	if code := f.PrintError(errTest("index is missing")); code != 1 {
		t.Errorf("PrintError = %d, want 1", code)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("Invalid JSON %q: %v", buf.String(), err)
	}
	if parsed["error"] != true || parsed["message"] != "index is missing" {
		t.Errorf("unexpected error document: %v", parsed)
	}
	if !strings.Contains(buf.String(), "\n  ") {
		t.Errorf("error document was not pretty-printed: %q", buf.String())
	}
}

func TestPrintError_MinimalJSONUsesTheAbbreviatedKeys(t *testing.T) {
	// --json --min is read by the MCP layer, which expects the same abbreviated
	// keys on an error document as on a success one.
	var buf bytes.Buffer
	f := New(true, true, &buf)

	if code := f.PrintError(errTest("boom")); code != 1 {
		t.Errorf("PrintError = %d, want 1", code)
	}
	out := strings.TrimSpace(buf.String())
	if strings.Contains(out, "\n") {
		t.Errorf("minimal error was not a single line: %q", out)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("Invalid JSON %q: %v", out, err)
	}
	if parsed["err"] != true || parsed["msg"] != "boom" {
		t.Errorf("unexpected minimal error document: %v", parsed)
	}
	if _, ok := parsed["message"]; ok {
		t.Errorf("full key names leaked into minimal mode: %v", parsed)
	}
}

func TestPrintError_TextModePrefixesUnlessMinimal(t *testing.T) {
	// Plain text keeps the "Error: " prefix a human scans for; --min drops it,
	// because the message alone is the whole payload.
	var buf bytes.Buffer
	if code := New(false, false, &buf).PrintError(errTest("no index")); code != 1 {
		t.Errorf("PrintError = %d, want 1", code)
	}
	if buf.String() != "Error: no index\n" {
		t.Errorf("text error = %q, want %q", buf.String(), "Error: no index\n")
	}

	buf.Reset()
	if code := New(false, true, &buf).PrintError(errTest("no index")); code != 1 {
		t.Errorf("PrintError = %d, want 1", code)
	}
	if buf.String() != "no index\n" {
		t.Errorf("minimal text error = %q, want %q", buf.String(), "no index\n")
	}
}

func TestPrintError_TextModeDivertsStdoutToStderr(t *testing.T) {
	// In text mode an error belongs on stderr so a caller piping stdout gets
	// results only. The redirect is keyed on the writer being os.Stdout, which
	// is what a real CLI passes.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	realStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = realStderr })

	f := New(false, false, os.Stdout)
	code := f.PrintError(errTest("diverted"))
	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	os.Stderr = realStderr

	captured, err := readAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if code != 1 {
		t.Errorf("PrintError = %d, want 1", code)
	}
	if captured != "Error: diverted\n" {
		t.Errorf("stderr got %q, want %q", captured, "Error: diverted\n")
	}
}

// errTest is a minimal error whose text is exactly what it was given, so a
// message assertion is not testing fmt's wrapping.
type errTest string

func (e errTest) Error() string { return string(e) }

func readAll(r *os.File) (string, error) {
	defer func() { _ = r.Close() }()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		return "", err
	}
	return buf.String(), nil
}
