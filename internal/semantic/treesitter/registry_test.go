package treesitter

import (
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

type fakeChunker struct{ exts []string }

func (f *fakeChunker) Chunk(string, []byte) ([]semantic.Chunk, error) { return nil, nil }
func (f *fakeChunker) SupportedExtensions() []string                  { return f.exts }

// unwrap returns the chunker inside the registry's fallback wrapper, so a
// test can check which language chunker an extension routes to.
func unwrap(c semantic.Chunker) semantic.Chunker {
	if w, ok := c.(*Fallback); ok {
		return w.Unwrap()
	}
	return c
}

// mustPanic runs fn and returns the panic message, failing the test if fn returns normally.
func mustPanic(t *testing.T, fn func()) string {
	t.Helper()
	var msg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				msg, _ = r.(string)
			}
		}()
		fn()
		t.Fatal("expected a panic, got none")
	}()
	return msg
}

// Defends the registry seam: a chunker added through the registry must reach
// the factory under every extension it claims, so a new language needs only
// its own file and never an edit to commands/index.go.
func TestRegistry_FakeChunkerReachesFactory(t *testing.T) {
	fake := &fakeChunker{exts: []string{"fake", "fk"}}
	r := NewRegistry()
	r.Add(func() semantic.Chunker { return fake })

	factory := semantic.NewChunkerFactory()
	r.RegisterInto(factory)

	for _, name := range []string{"a.fake", "dir/b.fk"} {
		got, ok := factory.GetByExtension(name)
		if !ok {
			t.Fatalf("GetByExtension(%q) found nothing", name)
		}
		if unwrap(got) != fake {
			t.Errorf("GetByExtension(%q) = %T, want the registered fake", name, got)
		}
	}
}

// Defends the self-registration in each chunker file: if a file drops its
// init() call, its language would silently fall out of the index.
func TestRegisterAll_RegistersEveryTreesitterChunker(t *testing.T) {
	factory := semantic.NewChunkerFactory()
	RegisterAll(factory)

	want := map[string]func(semantic.Chunker) bool{
		"js":  func(c semantic.Chunker) bool { _, ok := c.(*JSChunker); return ok },
		"ts":  func(c semantic.Chunker) bool { _, ok := c.(*TypeScriptChunker); return ok },
		"tsx": func(c semantic.Chunker) bool { _, ok := c.(*TypeScriptChunker); return ok },
		"mts": func(c semantic.Chunker) bool { _, ok := c.(*TypeScriptChunker); return ok },
		"cts": func(c semantic.Chunker) bool { _, ok := c.(*TypeScriptChunker); return ok },
		"py":  func(c semantic.Chunker) bool { _, ok := c.(*PythonChunker); return ok },
		"php": func(c semantic.Chunker) bool { _, ok := c.(*PHPChunker); return ok },
		"rs":  func(c semantic.Chunker) bool { _, ok := c.(*RustChunker); return ok },
	}
	for ext, isType := range want {
		c, ok := factory.GetChunker(ext)
		if !ok {
			t.Errorf("extension %q not registered", ext)
			continue
		}
		if !isType(unwrap(c)) {
			t.Errorf("extension %q registered to %T", ext, c)
		}
	}
}

// Defends against one chunker silently replacing another: two registry
// entries claiming one extension would leave whichever ran last, and the
// other language's files would be chunked by the wrong grammar.
func TestRegistry_DuplicateExtensionPanics(t *testing.T) {
	r := NewRegistry()
	r.Add(func() semantic.Chunker { return &fakeChunker{exts: []string{"a", "dup"}} })
	r.Add(func() semantic.Chunker { return &fakeChunker{exts: []string{"dup"}} })

	msg := mustPanic(t, func() { r.RegisterInto(semantic.NewChunkerFactory()) })
	if !strings.Contains(msg, `"dup"`) {
		t.Errorf("panic message %q does not name the extension", msg)
	}
}

// Defends the native Go chunker: a tree-sitter chunker claiming an extension
// the factory already serves (for example "go", whose chunker emits call
// edges) must fail loudly rather than replace it.
func TestRegistry_ExistingFactoryExtensionPanics(t *testing.T) {
	factory := semantic.NewChunkerFactory()
	factory.Register("go", &fakeChunker{exts: []string{"go"}})

	r := NewRegistry()
	r.Add(func() semantic.Chunker { return &fakeChunker{exts: []string{"go"}} })

	msg := mustPanic(t, func() { r.RegisterInto(factory) })
	if !strings.Contains(msg, `"go"`) {
		t.Errorf("panic message %q does not name the extension", msg)
	}
}
