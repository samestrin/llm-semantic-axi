package treesitter

import (
	"strconv"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// The line numbers asserted below are positions in this sample, so the sample
// is a literal here, with the expectations next to it.
const javaSample = `package com.example;

import java.util.List;

public interface Greeter {
    String greet(String name);
}

@Deprecated
public class UserService implements Greeter {
    private final List<String> names;

    public UserService(List<String> names) {
        this.names = names;
    }

    @Override
    public String greet(String name) {
        return "hi " + name;
    }

    static class Inner {
        void run() {}
    }
}

enum Color {
    RED, GREEN;

    String lower() {
        return name().toLowerCase();
    }
}

record Point(int x, int y) {
    Point {
        if (x < 0) throw new IllegalArgumentException();
    }

    int sum() {
        return x + y;
    }
}

@interface Marker {}
`

func TestJavaChunker_Extensions(t *testing.T) {
	// The extension is the dispatcher's registry key; "java" only.
	got := NewJavaChunker().SupportedExtensions()
	if len(got) != 1 || got[0] != "java" {
		t.Errorf("SupportedExtensions() = %v, want [java]", got)
	}
}

func TestJavaChunker_NamesTypesAndLineRanges(t *testing.T) {
	// Defends the task's "done when": each class, interface, enum, record and
	// method comes back with the right name, type and 1-based line range.
	// An off-by-one here makes every search hit open the wrong lines.
	chunks, err := NewJavaChunker().Chunk("Sample.java", []byte(javaSample))
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	byKey := map[string]semantic.Chunk{}
	for _, c := range chunks {
		byKey[c.Name+"/"+c.Type.String()+"/"+strconv.Itoa(c.StartLine)] = c
	}

	// UserService starts at its @Deprecated annotation (line 9), which is part
	// of the declaration's modifiers; greet likewise starts at @Override.
	want := []struct {
		name       string
		typ        semantic.ChunkType
		start, end int
	}{
		{"Greeter", semantic.ChunkInterface, 5, 7},
		{"greet", semantic.ChunkMethod, 6, 6},
		{"UserService", semantic.ChunkStruct, 9, 25},
		{"UserService", semantic.ChunkMethod, 13, 15},
		{"greet", semantic.ChunkMethod, 17, 20},
		{"Inner", semantic.ChunkStruct, 22, 24},
		{"run", semantic.ChunkMethod, 23, 23},
		{"Color", semantic.ChunkStruct, 27, 33},
		{"lower", semantic.ChunkMethod, 30, 32},
		{"Point", semantic.ChunkStruct, 35, 43},
		{"Point", semantic.ChunkMethod, 36, 38},
		{"sum", semantic.ChunkMethod, 40, 42},
		{"Marker", semantic.ChunkInterface, 45, 45},
	}
	for _, w := range want {
		c, ok := byKey[w.name+"/"+w.typ.String()+"/"+strconv.Itoa(w.start)]
		if !ok {
			t.Errorf("missing %s chunk %q in %v", w.typ, w.name, chunks)
			continue
		}
		if c.StartLine != w.start || c.EndLine != w.end {
			t.Errorf("%q (%s) lines = %d-%d, want %d-%d", w.name, w.typ, c.StartLine, c.EndLine, w.start, w.end)
		}
	}
	if len(chunks) != len(want) {
		t.Errorf("got %d chunks, want %d: %v", len(chunks), len(want), chunks)
	}
}

func TestJavaChunker_ConstructorsAndInterfaceMethods(t *testing.T) {
	// A constructor shares its class's name; it must still be chunked (as a
	// method), or "how is UserService built" has no answer in the index. The
	// compact record constructor is the same case. (A bodyless interface method
	// is chunked too: its signature is what answers "what does Greeter require".)
	chunks, err := NewJavaChunker().Chunk("Sample.java", []byte(javaSample))
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	var ctors []semantic.Chunk
	for _, c := range chunks {
		if c.Name == "UserService" && c.Type == semantic.ChunkMethod {
			ctors = append(ctors, c)
		}
		if c.Name == "Point" && c.Type == semantic.ChunkMethod {
			ctors = append(ctors, c)
		}
	}
	if len(ctors) != 2 {
		t.Fatalf("got %d constructor chunks, want 2: %v", len(ctors), ctors)
	}
	if ctors[0].StartLine != 13 || ctors[0].EndLine != 15 {
		t.Errorf("constructor lines = %d-%d, want 13-15", ctors[0].StartLine, ctors[0].EndLine)
	}
}

func TestJavaChunker_MethodKeepsItsAnnotation(t *testing.T) {
	// @Override sits in the method's modifiers; dropping it would strip the
	// one-line statement of what the method is for from the stored content.
	chunks, err := NewJavaChunker().Chunk("Sample.java", []byte(javaSample))
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	var greet semantic.Chunk
	for _, c := range chunks {
		if c.Name == "greet" && c.StartLine == 17 {
			greet = c
		}
	}
	if !strings.HasPrefix(greet.Content, "@Override") {
		t.Errorf("greet content lost its annotation: %q", greet.Content)
	}
	// The signature is what results display; "@Override" alone would hide the method.
	if greet.Signature != "public String greet(String name)" {
		t.Errorf("greet signature = %q", greet.Signature)
	}
	var got string
	for _, c := range chunks {
		if c.Name == "UserService" && c.Type == semantic.ChunkStruct {
			got = c.Signature
		}
	}
	if got != "public class UserService implements Greeter" {
		t.Errorf("annotated class signature = %q", got)
	}
}

func TestJavaChunker_ChunkFields(t *testing.T) {
	// Every chunk must be storable: ID, language tag, path, valid line range.
	chunks, err := NewJavaChunker().Chunk("Sample.java", []byte(javaSample))
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	for _, c := range chunks {
		if c.ID == "" || c.Language != "java" || c.FilePath != "Sample.java" ||
			c.StartLine <= 0 || c.EndLine < c.StartLine || c.Content == "" || c.Signature == "" {
			t.Errorf("bad chunk: %+v", c)
		}
	}
}

func TestJavaChunker_EmptySourceYieldsNoChunks(t *testing.T) {
	// Empty or declaration-free files are normal input: no error, no chunks.
	for _, src := range []string{"", "package a;\n"} {
		chunks, err := NewJavaChunker().Chunk("E.java", []byte(src))
		if err != nil || len(chunks) != 0 {
			t.Errorf("Chunk(%q) = %d chunks, err %v; want 0, nil", src, len(chunks), err)
		}
	}
}

func TestJavaChunker_SyntaxErrorIsReported(t *testing.T) {
	// A broken file must return ErrParseTree so the fallback wrapper can window
	// it instead of the walk silently passing a partial file off as whole.
	_, err := NewJavaChunker().Chunk("B.java", []byte("class A { void f( { \n"))
	if err == nil {
		t.Error("expected ErrParseTree for broken source")
	}
}

func TestJavaChunker_NamelessDeclarationIsDropped(t *testing.T) {
	// The name guard: a node without a name field must not become a chunk.
	c := NewJavaChunker()
	tree, err := ParseSource([]byte("package a;\n"), c.lang)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Release()
	var out []semantic.Chunk
	c.extractType(tree.RootNode(), []byte("package a;\n"), "a.java", &out, semantic.ChunkStruct)
	c.extractMethod(tree.RootNode(), []byte("package a;\n"), "a.java", &out)
	if len(out) != 0 {
		t.Errorf("emitted chunks for nameless nodes: %+v", out)
	}
}
