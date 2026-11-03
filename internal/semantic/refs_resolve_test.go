package semantic

import (
	"context"
	"testing"
)

// refTarget returns the resolved target ID for the single ref matching refName
// on the given chunk. Returns "" when unresolved.
func refTarget(t *testing.T, rs RefStorage, chunkID, refName string) string {
	t.Helper()
	refs, err := rs.GetRefs(context.Background(), chunkID)
	if err != nil {
		t.Fatalf("GetRefs(%s): %v", chunkID, err)
	}
	for _, r := range refs {
		if r.RefName == refName {
			return r.RefTargetID
		}
	}
	t.Fatalf("no ref named %q on chunk %s", refName, chunkID)
	return ""
}

// TestResolveRefs_QualifiedNames proves that package-qualified calls (pkg.Func)
// and method calls (recv.Method) resolve to the correct chunk, not merely to
// some chunk. Bare local calls must keep working, and a call with no matching
// chunk (an external dependency) must stay unresolved.
func TestResolveRefs_QualifiedNames(t *testing.T) {
	storage, cleanup := createTestSQLiteStorage(t)
	defer cleanup()

	ctx := context.Background()

	chunks := []ChunkWithEmbedding{
		{Chunk: Chunk{ID: "caller", FilePath: "a.go", Type: ChunkFunction, Name: "Caller", Content: "x", StartLine: 1, EndLine: 9, Language: "go"}, Embedding: []float32{0.1}},
		{Chunk: Chunk{ID: "target-method", FilePath: "b.go", Type: ChunkMethod, Name: "Method", Content: "x", StartLine: 1, EndLine: 3, Language: "go"}, Embedding: []float32{0.2}},
		{Chunk: Chunk{ID: "target-func", FilePath: "c.go", Type: ChunkFunction, Name: "Func", Content: "x", StartLine: 1, EndLine: 3, Language: "go"}, Embedding: []float32{0.3}},
		{Chunk: Chunk{ID: "target-local", FilePath: "a.go", Type: ChunkFunction, Name: "Local", Content: "x", StartLine: 20, EndLine: 22, Language: "go"}, Embedding: []float32{0.4}},
	}
	if err := storage.CreateBatch(ctx, chunks); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}

	rs := RefStorage(storage)

	refs := []ChunkRef{
		{ChunkID: "caller", RefType: RefCalls, RefName: "recv.Method"},
		{ChunkID: "caller", RefType: RefCalls, RefName: "pkg.Func"},
		{ChunkID: "caller", RefType: RefCalls, RefName: "Local"},
		{ChunkID: "caller", RefType: RefCalls, RefName: "fmt.Println"},
	}
	if err := rs.StoreRefs(ctx, refs); err != nil {
		t.Fatalf("StoreRefs: %v", err)
	}

	if err := rs.ResolveRefs(ctx); err != nil {
		t.Fatalf("ResolveRefs: %v", err)
	}

	cases := []struct {
		refName string
		want    string
		why     string
	}{
		{"recv.Method", "target-method", "method call must resolve to the method chunk"},
		{"pkg.Func", "target-func", "package-qualified call must resolve to the function chunk"},
		{"Local", "target-local", "bare local call must keep resolving"},
		{"fmt.Println", "", "external call with no matching chunk must stay unresolved"},
	}
	for _, tc := range cases {
		if got := refTarget(t, rs, "caller", tc.refName); got != tc.want {
			t.Errorf("%s: ref %q resolved to %q, want %q", tc.why, tc.refName, got, tc.want)
		}
	}

	// The correct target must also be reachable from the other direction.
	callers, err := rs.GetCallers(ctx, "target-method")
	if err != nil {
		t.Fatalf("GetCallers: %v", err)
	}
	if len(callers) != 1 || callers[0].ChunkID != "caller" {
		t.Errorf("GetCallers(target-method) = %+v, want exactly one caller %q", callers, "caller")
	}
}

// TestResolveRefs_AmbiguousStaysUnresolved proves resolution never guesses.
// When two chunks share a name, the edge must stay unresolved rather than
// pick one arbitrarily. A wrong edge is worse than no edge.
func TestResolveRefs_AmbiguousStaysUnresolved(t *testing.T) {
	storage, cleanup := createTestSQLiteStorage(t)
	defer cleanup()

	ctx := context.Background()

	chunks := []ChunkWithEmbedding{
		{Chunk: Chunk{ID: "caller", FilePath: "a.go", Type: ChunkFunction, Name: "Caller", Content: "x", StartLine: 1, EndLine: 9, Language: "go"}, Embedding: []float32{0.1}},
		{Chunk: Chunk{ID: "dup-a", FilePath: "a.go", Type: ChunkFunction, Name: "Dup", Content: "x", StartLine: 20, EndLine: 22, Language: "go"}, Embedding: []float32{0.2}},
		{Chunk: Chunk{ID: "dup-b", FilePath: "b.go", Type: ChunkFunction, Name: "Dup", Content: "x", StartLine: 1, EndLine: 3, Language: "go"}, Embedding: []float32{0.3}},
		{Chunk: Chunk{ID: "unique", FilePath: "c.go", Type: ChunkFunction, Name: "Unique", Content: "x", StartLine: 1, EndLine: 3, Language: "go"}, Embedding: []float32{0.4}},
	}
	if err := storage.CreateBatch(ctx, chunks); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}

	rs := RefStorage(storage)

	refs := []ChunkRef{
		{ChunkID: "caller", RefType: RefCalls, RefName: "Dup"},
		{ChunkID: "caller", RefType: RefCalls, RefName: "pkg.Dup"},
		{ChunkID: "caller", RefType: RefCalls, RefName: "Unique"},
	}
	if err := rs.StoreRefs(ctx, refs); err != nil {
		t.Fatalf("StoreRefs: %v", err)
	}

	if err := rs.ResolveRefs(ctx); err != nil {
		t.Fatalf("ResolveRefs: %v", err)
	}

	if got := refTarget(t, rs, "caller", "Dup"); got != "" {
		t.Errorf("ambiguous bare name resolved to %q, want unresolved (two chunks named Dup)", got)
	}
	if got := refTarget(t, rs, "caller", "pkg.Dup"); got != "" {
		t.Errorf("ambiguous qualified name resolved to %q, want unresolved (two chunks named Dup)", got)
	}
	// An unambiguous name alongside ambiguous ones must still resolve.
	if got := refTarget(t, rs, "caller", "Unique"); got != "unique" {
		t.Errorf("unambiguous name resolved to %q, want %q", got, "unique")
	}

	// Neither duplicate may claim the caller.
	for _, id := range []string{"dup-a", "dup-b"} {
		callers, err := rs.GetCallers(ctx, id)
		if err != nil {
			t.Fatalf("GetCallers(%s): %v", id, err)
		}
		if len(callers) != 0 {
			t.Errorf("GetCallers(%s) = %+v, want none (ambiguous edge must not be attributed)", id, callers)
		}
	}
}

// TestResolveRefs_ClearsDanglingTarget proves that when a target chunk goes
// away (a rename or deletion), the stale edge is cleared instead of pointing
// at a chunk that no longer exists.
func TestResolveRefs_ClearsDanglingTarget(t *testing.T) {
	storage, cleanup := createTestSQLiteStorage(t)
	defer cleanup()

	ctx := context.Background()

	chunks := []ChunkWithEmbedding{
		{Chunk: Chunk{ID: "caller", FilePath: "a.go", Type: ChunkFunction, Name: "Caller", Content: "x", StartLine: 1, EndLine: 9, Language: "go"}, Embedding: []float32{0.1}},
		{Chunk: Chunk{ID: "target", FilePath: "b.go", Type: ChunkFunction, Name: "Target", Content: "x", StartLine: 1, EndLine: 3, Language: "go"}, Embedding: []float32{0.2}},
	}
	if err := storage.CreateBatch(ctx, chunks); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}

	rs := RefStorage(storage)

	if err := rs.StoreRefs(ctx, []ChunkRef{
		{ChunkID: "caller", RefType: RefCalls, RefName: "Target"},
	}); err != nil {
		t.Fatalf("StoreRefs: %v", err)
	}
	if err := rs.ResolveRefs(ctx); err != nil {
		t.Fatalf("ResolveRefs: %v", err)
	}
	if got := refTarget(t, rs, "caller", "Target"); got != "target" {
		t.Fatalf("precondition: ref resolved to %q, want %q", got, "target")
	}

	// The target is renamed away: its chunk disappears.
	if err := storage.Delete(ctx, "target"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if err := rs.ResolveRefs(ctx); err != nil {
		t.Fatalf("ResolveRefs after delete: %v", err)
	}

	if got := refTarget(t, rs, "caller", "Target"); got != "" {
		t.Errorf("stale edge still points at %q after its target was removed, want cleared", got)
	}
}

// TestResolveRefs_OnlyWithinCallerLanguage defends against a Python run()
// linking to the lone Go chunk named run: an edge must resolve only to a chunk
// of the calling chunk's language, while a same-language target still resolves.
func TestResolveRefs_OnlyWithinCallerLanguage(t *testing.T) {
	storage, cleanup := createTestSQLiteStorage(t)
	defer cleanup()

	ctx := context.Background()
	chunks := []ChunkWithEmbedding{
		{Chunk: Chunk{ID: "py-caller", FilePath: "a.py", Type: ChunkFunction, Name: "main", Content: "x", StartLine: 1, EndLine: 9, Language: "python"}, Embedding: []float32{0.1}},
		{Chunk: Chunk{ID: "go-run", FilePath: "b.go", Type: ChunkFunction, Name: "run", Content: "x", StartLine: 1, EndLine: 3, Language: "go"}, Embedding: []float32{0.2}},
		{Chunk: Chunk{ID: "py-helper", FilePath: "c.py", Type: ChunkFunction, Name: "helper", Content: "x", StartLine: 1, EndLine: 3, Language: "python"}, Embedding: []float32{0.3}},
	}
	if err := storage.CreateBatch(ctx, chunks); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	rs := RefStorage(storage)
	if err := rs.StoreRefs(ctx, []ChunkRef{
		{ChunkID: "py-caller", RefType: RefCalls, RefName: "run"},
		{ChunkID: "py-caller", RefType: RefCalls, RefName: "helper"},
	}); err != nil {
		t.Fatalf("StoreRefs: %v", err)
	}
	if err := rs.ResolveRefs(ctx); err != nil {
		t.Fatalf("ResolveRefs: %v", err)
	}

	if got := refTarget(t, rs, "py-caller", "run"); got != "" {
		t.Errorf("python run() resolved to %q, want unresolved (only a Go run exists)", got)
	}
	if got := refTarget(t, rs, "py-caller", "helper"); got != "py-helper" {
		t.Errorf("same-language call resolved to %q, want py-helper", got)
	}
}

// TestResolveRefs_JSTSFamily defends two things at once. A .js call to a
// function defined in .ts (or .ts to .tsx) must link, because JS and TS import
// each other freely and treating each extension as its own language would leave
// every such edge unresolved. And the family must not leak: a .ts call must not
// link to a lone Go chunk, and a .py call must still resolve only to Python.
func TestResolveRefs_JSTSFamily(t *testing.T) {
	storage, cleanup := createTestSQLiteStorage(t)
	defer cleanup()

	ctx := context.Background()
	chunk := func(id, path, name, lang string) ChunkWithEmbedding {
		return ChunkWithEmbedding{Chunk: Chunk{ID: id, FilePath: path, Type: ChunkFunction, Name: name, Content: "x", StartLine: 1, EndLine: 3, Language: lang}, Embedding: []float32{0.1}}
	}
	chunks := []ChunkWithEmbedding{
		chunk("js-caller", "a.js", "main", "js"),
		chunk("ts-format", "b.ts", "format", "ts"),
		chunk("ts-caller", "c.ts", "render", "ts"),
		chunk("tsx-button", "d.tsx", "Button", "tsx"),
		chunk("go-run", "e.go", "run", "go"),
		chunk("py-caller", "f.py", "entry", "py"),
		chunk("py-load", "g.py", "load", "py"),
		chunk("mjs-load", "h.mjs", "load", "mjs"),
	}
	if err := storage.CreateBatch(ctx, chunks); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	rs := RefStorage(storage)
	if err := rs.StoreRefs(ctx, []ChunkRef{
		{ChunkID: "js-caller", RefType: RefCalls, RefName: "format"},
		{ChunkID: "ts-caller", RefType: RefCalls, RefName: "Button"},
		{ChunkID: "ts-caller", RefType: RefCalls, RefName: "run"},
		{ChunkID: "py-caller", RefType: RefCalls, RefName: "load"},
	}); err != nil {
		t.Fatalf("StoreRefs: %v", err)
	}
	if err := rs.ResolveRefs(ctx); err != nil {
		t.Fatalf("ResolveRefs: %v", err)
	}

	if got := refTarget(t, rs, "js-caller", "format"); got != "ts-format" {
		t.Errorf(".js call to format resolved to %q, want ts-format", got)
	}
	if got := refTarget(t, rs, "ts-caller", "Button"); got != "tsx-button" {
		t.Errorf(".ts call to Button resolved to %q, want tsx-button", got)
	}
	if got := refTarget(t, rs, "ts-caller", "run"); got != "" {
		t.Errorf(".ts call to run resolved to %q, want unresolved (only a Go run exists)", got)
	}
	if got := refTarget(t, rs, "py-caller", "load"); got != "py-load" {
		t.Errorf(".py call to load resolved to %q, want py-load (the .mjs load is another language)", got)
	}
}
