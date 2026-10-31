package commands

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

const csharpIndexSource = `namespace Shop
{
    public interface IRepo
    {
        int Find(int id);
    }

    public class Service : IRepo
    {
        public int Find(int id)
        {
            return id;
        }
    }

    public struct Money
    {
        public decimal Amount;
    }

    public record Order(int Id);
}
`

// TestRunIndexStoresNamedRowsForCSharp is task T3's "done when" in the
// database: a .cs file indexes as named namespace, interface, class, struct,
// record and method rows with correct line ranges, and is not reported as a
// parse fallback. Before the chunker, .cs was skipped and no rows existed.
func TestRunIndexStoresNamedRowsForCSharp(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "Shop.cs"), csharpIndexSource)

	out := runIndexIn(t, ".", indexOpts{skipCalibration: true})
	if strings.Contains(out, "text windows") {
		t.Errorf("a clean C# file reported a fallback:\n%s", out)
	}

	rows := storedChunks(t)["Shop.cs"]
	want := []struct {
		name       string
		typ        semantic.ChunkType
		start, end int
	}{
		{"Shop", semantic.ChunkFile, 1, 22},
		{"IRepo", semantic.ChunkInterface, 3, 6},
		{"Find", semantic.ChunkMethod, 5, 5},
		{"Service", semantic.ChunkStruct, 8, 14},
		{"Find", semantic.ChunkMethod, 10, 13},
		{"Money", semantic.ChunkStruct, 16, 19},
		{"Order", semantic.ChunkStruct, 21, 21},
	}
	for _, w := range want {
		found := false
		for _, c := range rows {
			if c.Name == w.name && c.Type == w.typ && c.StartLine == w.start {
				found = true
				if c.EndLine != w.end {
					t.Errorf("%s (%v) = lines %d-%d, want %d-%d", w.name, w.typ, c.StartLine, c.EndLine, w.start, w.end)
				}
			}
		}
		if !found {
			t.Errorf("no stored %v row %q at line %d; rows = %+v", w.typ, w.name, w.start, rows)
		}
	}
	if len(rows) != len(want) {
		t.Errorf("stored %d rows for Shop.cs, want %d: %+v", len(rows), len(want), rows)
	}
}
