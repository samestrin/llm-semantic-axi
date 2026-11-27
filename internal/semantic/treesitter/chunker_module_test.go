package treesitter

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"github.com/samestrin/llm-semantic-axi/v2/internal/semantic"
)

// chunkJSOrTS chunks src with the chunker the path's extension selects.
func chunkJSOrTS(path string, src []byte) ([]semantic.Chunk, error) {
	switch semantic.LanguageFromExtension(path) {
	case "ts", "tsx", "mts", "cts":
		return NewTypeScriptChunker().Chunk(path, src)
	}
	return NewJSChunker().Chunk(path, src)
}

// moduleChunks returns the chunks named moduleChunkName.
func moduleChunks(chunks []semantic.Chunk) []semantic.Chunk {
	var got []semantic.Chunk
	for _, c := range chunks {
		if c.Name == moduleChunkName {
			got = append(got, c)
		}
	}
	return got
}

// Defends a plain JS or TS script being indexed as something: a file of
// top-level statements and no function produced no chunk at all, so its code
// could not be searched and a module-level call had no chunk to belong to.
// The `<module>` chunk spans the whole file, and its ID is derived from path,
// name and start line like every other chunk's, so a re-index finds it again.
func TestJSTSChunker_PlainScriptGetsOneModuleChunk(t *testing.T) {
	src := "const PORT = 8080;\ninit();\nconsole.log(PORT);\n"
	for _, path := range []string{"a.js", "a.jsx", "a.mjs", "a.cjs", "a.ts", "a.tsx", "a.mts", "a.cts"} {
		chunks, err := chunkJSOrTS(path, []byte(src))
		if err != nil {
			t.Fatalf("Chunk(%s) returned %v", path, err)
		}
		if len(chunks) != 1 {
			t.Fatalf("Chunk(%s) = %d chunks, want exactly the module chunk: %+v", path, len(chunks), chunks)
		}
		m := chunks[0]
		if m.Name != "<module>" || m.Type != semantic.ChunkFile {
			t.Errorf("%s: chunk is %q (%s), want <module> (file)", path, m.Name, m.Type)
		}
		if m.StartLine != 1 || m.EndLine != 3 {
			t.Errorf("%s: <module> spans %d-%d, want the whole file 1-3", path, m.StartLine, m.EndLine)
		}
		if m.Content != src || m.FilePath != path || m.Language != semantic.LanguageFromExtension(path) {
			t.Errorf("%s: <module> = %+v, want the whole file's content, its path and language", path, m)
		}
		sum := sha256.Sum256([]byte(path + ":<module>:1"))
		if want := fmt.Sprintf("%x", sum[:8]); m.ID != want {
			t.Errorf("%s: <module> ID = %q, want %q (path:name:start line)", path, m.ID, want)
		}
		again, _ := chunkJSOrTS(path, []byte(src))
		if again[0].ID != m.ID {
			t.Errorf("%s: <module> ID changed between runs: %q then %q", path, m.ID, again[0].ID)
		}
	}
}

// Defends every existing chunk staying where it was: the declaration chunks'
// IDs are what stored edges and a re-index key on, so the module chunk must be
// added beside them, never by moving or renaming one. The IDs below were taken
// from main before the module chunk existed.
func TestJSTSChunker_ModuleChunkLeavesDeclarationIDsUnchanged(t *testing.T) {
	src := "import { readFile } from \"./io\";\n\nfunction helper() {\n  return 1;\n}\n\nconst run = () => helper();\n\nclass Service {\n  start() {\n    return this.stop();\n  }\n}\n\nhelper();\n"
	cases := map[string][]string{
		"a.js": {"helper c15cf5852396a658 3 5", "run b1eb63e8c62b1df2 7 7", "Service 2ec3eb85e9293fa3 9 13", "start 7b2d0d8752094e85 10 12"},
		"a.ts": {"helper 8fc38a24b53f4558 3 5", "run 9d904324fa6f87fd 7 7", "Service 1f9c822e10994799 9 13", "start 6600a5493abe2393 10 12"},
	}
	for path, want := range cases {
		chunks, err := chunkJSOrTS(path, []byte(src))
		if err != nil {
			t.Fatalf("Chunk(%s) returned %v", path, err)
		}
		var got []string
		for _, c := range chunks {
			if c.Name != moduleChunkName {
				got = append(got, fmt.Sprintf("%s %s %d %d", c.Name, c.ID, c.StartLine, c.EndLine))
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s declaration chunks:\n  got  %q\n  want %q", path, got, want)
		}
		if m := moduleChunks(chunks); len(m) != 1 || m[0].StartLine != 1 || m[0].EndLine != 15 {
			t.Errorf("%s: want one <module> chunk spanning 1-15, got %+v", path, m)
		}
	}
}

// Defends `<module>` meaning "this file parsed": a file the grammar could not
// parse falls back to text windows for the lines the walk missed, and a
// whole-file chunk there would cover every line, so no window would be cut and
// the broken file would pass for a parsed one.
func TestJSTSChunker_FallenBackFileGetsNoModuleChunk(t *testing.T) {
	src := "function ok() {\n  return 1;\n}\n\nconst broken = (;\ninit();\n"
	for _, path := range []string{"a.js", "a.ts", "a.tsx"} {
		chunks, err := chunkJSOrTS(path, []byte(src))
		if !errors.Is(err, ErrParseTree) {
			t.Fatalf("Chunk(%s) err = %v, want ErrParseTree", path, err)
		}
		if m := moduleChunks(chunks); len(m) != 0 {
			t.Errorf("Chunk(%s) on a broken file emitted %+v", path, m)
		}

		var inner semantic.Chunker = NewJSChunker()
		if path != "a.js" {
			inner = NewTypeScriptChunker()
		}
		got, reason, err := WithFallback(inner).ChunkWithFallback(path, []byte(src))
		if err != nil || reason != semantic.FallbackParseFailed {
			t.Fatalf("ChunkWithFallback(%s) = reason %v, err %v; want a parse fallback", path, reason, err)
		}
		if m := moduleChunks(got); len(m) != 0 {
			t.Errorf("ChunkWithFallback(%s) emitted %+v", path, m)
		}
		windows := 0
		for _, c := range got {
			if c.Type == semantic.ChunkFile {
				windows++
			}
		}
		if windows == 0 {
			t.Errorf("ChunkWithFallback(%s) cut no text window for the broken lines: %+v", path, got)
		}
	}
}

// Defends a blank file staying chunkless: there is no code in it to search or
// to call from, and an empty chunk would be embedded as nothing.
func TestJSTSChunker_BlankFileGetsNoModuleChunk(t *testing.T) {
	for _, src := range []string{"", "\n\n", "  \n\t\n"} {
		for _, path := range []string{"a.js", "a.ts"} {
			chunks, err := chunkJSOrTS(path, []byte(src))
			if err != nil {
				t.Errorf("Chunk(%s, %q) returned %v", path, src, err)
			}
			if len(chunks) != 0 {
				t.Errorf("Chunk(%s, %q) = %+v, want none", path, src, chunks)
			}
		}
	}
}

// Defends the last line of a file with no trailing newline being inside the
// module chunk.
func TestJSTSChunker_ModuleChunkEndsOnTheLastLineWithoutATrailingNewline(t *testing.T) {
	for _, path := range []string{"a.js", "a.ts"} {
		chunks, err := chunkJSOrTS(path, []byte("a();\nb();"))
		if err != nil {
			t.Fatalf("Chunk(%s) returned %v", path, err)
		}
		if m := moduleChunks(chunks); len(m) != 1 || m[0].EndLine != 2 {
			t.Errorf("Chunk(%s): want one <module> ending on line 2, got %+v", path, m)
		}
	}
}
