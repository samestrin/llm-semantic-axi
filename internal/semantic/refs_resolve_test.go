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

// TestResolveRefs_ThisCallStaysInItsClass defends against a `this.` call
// linking by bare name: with two classes in one file, each with get, plus a
// top-level get there, A's this.get() means A's get and nothing else. A
// `this.` call from outside any class names no class, so it stays unresolved
// rather than picking the one lone match. Both stores that resolve edges run
// it: SQLite, and the sidecar index behind Qdrant.
func TestResolveRefs_ThisCallStaysInItsClass(t *testing.T) {
	chunks := []Chunk{
		{ID: "class-a", FilePath: "m.ts", Type: ChunkStruct, Name: "A", Content: "x", StartLine: 1, EndLine: 10, Language: "ts"},
		{ID: "a-get", FilePath: "m.ts", Type: ChunkMethod, Name: "get", Content: "x", StartLine: 2, EndLine: 4, Language: "ts"},
		{ID: "a-run", FilePath: "m.ts", Type: ChunkMethod, Name: "run", Content: "x", StartLine: 5, EndLine: 9, Language: "ts"},
		{ID: "class-b", FilePath: "m.ts", Type: ChunkStruct, Name: "B", Content: "x", StartLine: 12, EndLine: 20, Language: "ts"},
		{ID: "b-get", FilePath: "m.ts", Type: ChunkMethod, Name: "get", Content: "x", StartLine: 13, EndLine: 15, Language: "ts"},
		{ID: "top-get", FilePath: "m.ts", Type: ChunkFunction, Name: "get", Content: "x", StartLine: 22, EndLine: 24, Language: "ts"},
		{ID: "loose", FilePath: "n.ts", Type: ChunkFunction, Name: "loose", Content: "x", StartLine: 1, EndLine: 3, Language: "ts"},
		{ID: "lone", FilePath: "o.ts", Type: ChunkMethod, Name: "lone", Content: "x", StartLine: 2, EndLine: 3, Language: "ts"},
		{ID: "lone-class", FilePath: "o.ts", Type: ChunkStruct, Name: "C", Content: "x", StartLine: 1, EndLine: 4, Language: "ts"},
		{ID: "bare", FilePath: "p.ts", Type: ChunkFunction, Name: "bare", Content: "x", StartLine: 1, EndLine: 3, Language: "ts"},
		{ID: "bare-target", FilePath: "q.ts", Type: ChunkFunction, Name: "target", Content: "x", StartLine: 1, EndLine: 3, Language: "ts"},
	}
	refs := []ChunkRef{
		{ChunkID: "a-run", RefType: RefCalls, RefName: "this.get"},
		{ChunkID: "loose", RefType: RefCalls, RefName: "this.lone"},
		{ChunkID: "bare", RefType: RefCalls, RefName: "target"},
	}
	cases := []struct{ chunkID, refName, want, why string }{
		{"a-run", "this.get", "a-get", "this.get in A must link to A's get, not B's or the function"},
		{"loose", "this.lone", "", "this. outside a class names no class and must stay unresolved"},
		{"bare", "target", "bare-target", "a bare name keeps resolving across files"},
	}

	ctx := context.Background()
	stores := map[string]func(t *testing.T) RefStorage{
		"sqlite": func(t *testing.T) RefStorage {
			storage, cleanup := createTestSQLiteStorage(t)
			t.Cleanup(cleanup)
			var batch []ChunkWithEmbedding
			for _, c := range chunks {
				batch = append(batch, ChunkWithEmbedding{Chunk: c, Embedding: []float32{0.1}})
			}
			if err := storage.CreateBatch(ctx, batch); err != nil {
				t.Fatalf("CreateBatch: %v", err)
			}
			return storage
		},
		"lexical": func(t *testing.T) RefStorage {
			idx, err := NewLexicalIndex(":memory:", 4)
			if err != nil {
				t.Fatalf("NewLexicalIndex: %v", err)
			}
			t.Cleanup(func() { idx.Close() })
			if err := idx.IndexBatch(ctx, chunks); err != nil {
				t.Fatalf("IndexBatch: %v", err)
			}
			return idx
		},
	}
	for name, open := range stores {
		t.Run(name, func(t *testing.T) {
			rs := open(t)
			if err := rs.StoreRefs(ctx, refs); err != nil {
				t.Fatalf("StoreRefs: %v", err)
			}
			if err := rs.ResolveRefs(ctx); err != nil {
				t.Fatalf("ResolveRefs: %v", err)
			}
			for _, tc := range cases {
				if got := refTarget(t, rs, tc.chunkID, tc.refName); got != tc.want {
					t.Errorf("%s: %q resolved to %q, want %q", tc.why, tc.refName, got, tc.want)
				}
			}
		})
	}
}

// TestResolveRefs_ImportsNeverResolve defends against an imports edge linking
// to a chunk that merely shares its name. An import names a module, and a
// module has no chunk: `import debounce from "lodash/debounce"` is recorded as
// imports debounce, and before this rule it linked to any lone local function
// named debounce, so refs showed a confident wrong target. Go is held to the
// same rule on purpose: `import "net/http"` is recorded as imports http and
// has no more claim on a lone func http. A calls edge to the same name still
// resolves, so the rule is the edge type and not the name.
func TestResolveRefs_ImportsNeverResolve(t *testing.T) {
	chunks := []Chunk{
		{ID: "ts-main", FilePath: "main.ts", Type: ChunkFunction, Name: "main", Content: "x", StartLine: 3, EndLine: 5, Language: "ts"},
		{ID: "ts-debounce", FilePath: "local.ts", Type: ChunkFunction, Name: "debounce", Content: "x", StartLine: 1, EndLine: 3, Language: "ts"},
		{ID: "go-main", FilePath: "main.go", Type: ChunkFunction, Name: "Main", Content: "x", StartLine: 5, EndLine: 9, Language: "go"},
		{ID: "go-http", FilePath: "local.go", Type: ChunkFunction, Name: "http", Content: "x", StartLine: 1, EndLine: 3, Language: "go"},
	}
	refs := []ChunkRef{
		{ChunkID: "ts-main", RefType: RefImports, RefName: "debounce"},
		{ChunkID: "go-main", RefType: RefImports, RefName: "http"},
		{ChunkID: "go-main", RefType: RefCalls, RefName: "http"},
	}
	cases := []struct {
		chunkID string
		refType RefType
		refName string
		want    string
		why     string
	}{
		{"ts-main", RefImports, "debounce", "", "a TS import names a module, not the lone local function debounce"},
		{"go-main", RefImports, "http", "", "a Go import names a package, not the lone func http"},
		{"go-main", RefCalls, "http", "go-http", "a call to the same name must keep resolving"},
	}

	ctx := context.Background()
	stores := map[string]func(t *testing.T) RefStorage{
		"sqlite": func(t *testing.T) RefStorage {
			storage, cleanup := createTestSQLiteStorage(t)
			t.Cleanup(cleanup)
			var batch []ChunkWithEmbedding
			for _, c := range chunks {
				batch = append(batch, ChunkWithEmbedding{Chunk: c, Embedding: []float32{0.1}})
			}
			if err := storage.CreateBatch(ctx, batch); err != nil {
				t.Fatalf("CreateBatch: %v", err)
			}
			return storage
		},
		"lexical": func(t *testing.T) RefStorage {
			idx, err := NewLexicalIndex(":memory:", 4)
			if err != nil {
				t.Fatalf("NewLexicalIndex: %v", err)
			}
			t.Cleanup(func() { idx.Close() })
			if err := idx.IndexBatch(ctx, chunks); err != nil {
				t.Fatalf("IndexBatch: %v", err)
			}
			return idx
		},
	}
	for name, open := range stores {
		t.Run(name, func(t *testing.T) {
			rs := open(t)
			if err := rs.StoreRefs(ctx, refs); err != nil {
				t.Fatalf("StoreRefs: %v", err)
			}
			if err := rs.ResolveRefs(ctx); err != nil {
				t.Fatalf("ResolveRefs: %v", err)
			}
			for _, tc := range cases {
				stored, err := rs.GetRefs(ctx, tc.chunkID)
				if err != nil {
					t.Fatalf("GetRefs(%s): %v", tc.chunkID, err)
				}
				found := false
				for _, r := range stored {
					if r.RefType != tc.refType || r.RefName != tc.refName {
						continue
					}
					found = true
					if r.RefTargetID != tc.want {
						t.Errorf("%s: %s %q resolved to %q, want %q", tc.why, tc.refType, tc.refName, r.RefTargetID, tc.want)
					}
				}
				if !found {
					t.Fatalf("no %s %q ref on %s", tc.refType, tc.refName, tc.chunkID)
				}
			}
		})
	}
}
