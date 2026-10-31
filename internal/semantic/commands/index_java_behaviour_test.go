package commands

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

const javaFixture = `package demo;

public interface Shape {
    double area();
}

public class Circle implements Shape {
    private final double r;

    public Circle(double r) {
        this.r = r;
    }

    public double area() {
        return Math.PI * r * r;
    }
}

enum Unit {
    CM, M;
}

record Pair(int a, int b) {
    int total() {
        return a + b;
    }
}
`

// TestRunIndexStoresJavaChunksWithNamesAndLines is task T4's "done when":
// before the Java chunker, a .java file was skipped (no chunker for the
// extension), so the database held no rows for it. The assertions read the
// stored rows, not the exit status.
func TestRunIndexStoresJavaChunksWithNamesAndLines(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "Shapes.java"), javaFixture)

	runIndexIn(t, ".", indexOpts{skipCalibration: true})

	type row struct {
		typ        semantic.ChunkType
		start, end int
	}
	got := map[string]row{}
	for _, c := range storedChunks(t)["Shapes.java"] {
		got[c.Name+"/"+c.Type.String()+"/"+strconv.Itoa(c.StartLine)] = row{c.Type, c.StartLine, c.EndLine}
	}
	want := map[string]row{
		"Shape/interface/3": {semantic.ChunkInterface, 3, 5},
		"area/method/4":     {semantic.ChunkMethod, 4, 4},
		"Circle/struct/7":   {semantic.ChunkStruct, 7, 17},
		"Circle/method/10":  {semantic.ChunkMethod, 10, 12},
		"Unit/struct/19":    {semantic.ChunkStruct, 19, 21},
		"Pair/struct/23":    {semantic.ChunkStruct, 23, 27},
		"area/method/14":    {semantic.ChunkMethod, 14, 16},
		"total/method/24":   {semantic.ChunkMethod, 24, 26},
	}
	for k, w := range want {
		if g, ok := got[k]; !ok {
			t.Errorf("no stored row %q; have %v", k, got)
		} else if g != w {
			t.Errorf("row %q = %+v, want %+v", k, g, w)
		}
	}
}
