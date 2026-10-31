package commands

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// TestRunIndexStoresNamedRowsForCppAndACHeader is task T6's "done when" in the
// database: a .cpp file and a C header named .h both index as named chunks
// with correct line ranges, and neither is reported as a parse fallback. Before
// the chunker, both extensions were skipped and no rows existed at all.
func TestRunIndexStoresNamedRowsForCppAndACHeader(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "shapes.cpp"),
		"namespace geo {\nstruct Point {\n    int x;\n};\n}\n\nint Point_len(int a) {\n    return a;\n}\n")
	writeFile(t, filepath.Join(dir, "legacy.h"),
		"#ifndef L_H\n#define L_H\nstatic int twice(int a) {\n    return a * 2;\n}\n#endif\n")

	out := runIndexIn(t, ".", indexOpts{skipCalibration: true})
	if strings.Contains(out, "text windows") {
		t.Errorf("clean C++ fixtures reported a fallback:\n%s", out)
	}

	byFile := storedChunks(t)
	want := map[string]map[string][3]int{
		"shapes.cpp": {"geo": {1, 5, int(semantic.ChunkFile)}, "Point": {2, 4, int(semantic.ChunkStruct)}, "Point_len": {7, 9, int(semantic.ChunkFunction)}},
		"legacy.h":   {"twice": {3, 5, int(semantic.ChunkFunction)}},
	}
	for file, names := range want {
		got := map[string]semantic.Chunk{}
		for _, c := range byFile[file] {
			got[c.Name] = c
		}
		for name, w := range names {
			c, ok := got[name]
			if !ok {
				t.Errorf("%s: no stored row named %q; rows = %+v", file, name, byFile[file])
				continue
			}
			if c.StartLine != w[0] || c.EndLine != w[1] || int(c.Type) != w[2] {
				t.Errorf("%s %s = lines %d-%d type %v, want %d-%d type %v", file, name, c.StartLine, c.EndLine, c.Type, w[0], w[1], semantic.ChunkType(w[2]))
			}
		}
	}
}
