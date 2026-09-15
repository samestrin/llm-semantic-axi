package treesitter

import (
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// The PHP fixture lives in the test file rather than testdata/ so the sample
// and the expectations pinned against it stay in one place — every construct
// below is here because a branch of chunker_php.go depends on it.
const phpSample = `<?php

namespace App;

interface Greeter {
    public function greet(string $name): string;
}

trait Loggable {
    public function log(string $msg): void {
        echo $msg;
    }
}

class UserService implements Greeter {
    use Loggable;

    private $db;

    public function __construct($db) {
        $this->db = $db;
    }

    public function greet(string $name): string {
        return "hi " . $name;
    }
}

function standalone_helper(int $x): int {
    return $x * 2;
}
`

// chunksByName indexes chunks by name so a test can assert on one construct
// without depending on the order the walker happens to visit siblings in.
func chunksByName(chunks []semantic.Chunk) map[string]semantic.Chunk {
	byName := make(map[string]semantic.Chunk, len(chunks))
	for _, c := range chunks {
		byName[c.Name] = c
	}
	return byName
}

// --- PHP Tests ---

func TestPHPChunker_Extensions(t *testing.T) {
	// The extension list is the registry key a dispatcher routes on, so the
	// exact set is the contract, not just its length.
	got := NewPHPChunker().SupportedExtensions()
	want := []string{"php", "phtml", "php5", "php7"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("SupportedExtensions() = %v, want %v", got, want)
	}
}

func TestPHPChunker_ExtractsEachTopLevelConstruct(t *testing.T) {
	// One assertion per switch arm in walkNode: interface, trait, class and a
	// bare function each map to a specific ChunkType, and getting the type wrong
	// is what makes a search filter (--type function) silently miss results.
	chunks, err := NewPHPChunker().Chunk("sample.php", []byte(phpSample))
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	byName := chunksByName(chunks)

	for name, want := range map[string]semantic.ChunkType{
		"Greeter":           semantic.ChunkInterface,
		"Loggable":          semantic.ChunkStruct,
		"UserService":       semantic.ChunkStruct,
		"standalone_helper": semantic.ChunkFunction,
	} {
		c, ok := byName[name]
		if !ok {
			t.Errorf("expected to find %q, got %v", name, chunks)
			continue
		}
		if c.Type != want {
			t.Errorf("%q has type %v, want %v", name, c.Type, want)
		}
	}
}

func TestPHPChunker_MethodsInheritTheEnclosingClass(t *testing.T) {
	// The enclosingClass argument is the only thing separating ChunkMethod from
	// ChunkFunction. A method reached through extractClass must come back as a
	// method, while the identical `function` keyword at file scope must not.
	chunks, err := NewPHPChunker().Chunk("sample.php", []byte(phpSample))
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	byName := chunksByName(chunks)

	for _, name := range []string{"__construct", "greet"} {
		c, ok := byName[name]
		if !ok {
			t.Fatalf("expected class method %q in %v", name, chunks)
		}
		if c.Type != semantic.ChunkMethod {
			t.Errorf("%q has type %v, want ChunkMethod", name, c.Type)
		}
	}

	// A trait body is walked with the trait name as the enclosing type, so its
	// methods are methods too.
	if c, ok := byName["log"]; !ok {
		t.Error("expected trait method 'log'")
	} else if c.Type != semantic.ChunkMethod {
		t.Errorf("trait method 'log' has type %v, want ChunkMethod", c.Type)
	}

	if c := byName["standalone_helper"]; c.Type != semantic.ChunkFunction {
		t.Errorf("file-scope function has type %v, want ChunkFunction", c.Type)
	}
}

func TestPHPChunker_ChunkFields(t *testing.T) {
	// Every chunk must be storable: a non-empty ID to key on, the language tag
	// the index filters by, and a line range a caller can open in an editor.
	chunks, err := NewPHPChunker().Chunk("sample.php", []byte(phpSample))
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("Expected chunks, got none")
	}
	for _, c := range chunks {
		if c.ID == "" {
			t.Errorf("Chunk %q has empty ID", c.Name)
		}
		if c.Language != "php" {
			t.Errorf("Chunk %q has language %q, want 'php'", c.Name, c.Language)
		}
		if c.FilePath != "sample.php" {
			t.Errorf("Chunk %q has FilePath %q, want 'sample.php'", c.Name, c.FilePath)
		}
		if c.StartLine <= 0 || c.EndLine < c.StartLine {
			t.Errorf("Chunk %q has invalid line range: %d-%d", c.Name, c.StartLine, c.EndLine)
		}
		if c.Content == "" {
			t.Errorf("Chunk %q has empty Content", c.Name)
		}
		if c.Signature == "" {
			t.Errorf("Chunk %q has empty Signature", c.Name)
		}
	}
}

func TestPHPChunker_SignatureStopsAtTheFirstLine(t *testing.T) {
	// A signature is what gets embedded and shown in results; carrying the whole
	// body would both blow the embedding budget and make every hit look alike.
	chunks, err := NewPHPChunker().Chunk("sample.php", []byte(phpSample))
	if err != nil {
		t.Fatalf("Chunk failed: %v", err)
	}
	byName := chunksByName(chunks)

	if got := byName["UserService"].Signature; got != "class UserService implements Greeter" {
		t.Errorf("class signature = %q, want %q", got, "class UserService implements Greeter")
	}
	if got := byName["greet"].Signature; got != "public function greet(string $name): string" {
		t.Errorf("method signature = %q, want %q", got, "public function greet(string $name): string")
	}
}

func TestPHPChunker_EmptySourceYieldsNoChunks(t *testing.T) {
	// An empty or declaration-only file is a normal input for a bulk indexer.
	// It must return an empty result, not an error and not a nil-deref.
	for _, src := range []string{"", "<?php\n", "<?php\n$x = 1;\n"} {
		chunks, err := NewPHPChunker().Chunk("empty.php", []byte(src))
		if err != nil {
			t.Errorf("Chunk(%q) returned error: %v", src, err)
		}
		if len(chunks) != 0 {
			t.Errorf("Chunk(%q) = %d chunks, want 0", src, len(chunks))
		}
	}
}
