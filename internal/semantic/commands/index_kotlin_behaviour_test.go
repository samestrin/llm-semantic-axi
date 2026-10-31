package commands

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

const kotlinFixture = `package demo

interface Shape {
    fun area(): Double
}

class Circle(val r: Double) : Shape {
    override fun area(): Double {
        return 3.14 * r * r
    }

    companion object {
        fun unit() = Circle(1.0)
    }
}

object Registry {
    fun register(s: Shape) {}
}

fun describe(s: Shape): String {
    return "area " + s.area()
}
`

const kotlinScriptFixture = `val name = "demo"

fun hello() = println(name)
`

// TestRunIndexStoresKotlinChunksWithNamesAndLines is task T2's "done when":
// before the Kotlin chunker, .kt and .kts files were skipped (no chunker for
// the extension), so the database held no rows for them. The assertions read
// the stored rows, not the exit status.
func TestRunIndexStoresKotlinChunksWithNamesAndLines(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "Shapes.kt"), kotlinFixture)
	writeFile(t, filepath.Join(dir, "build.gradle.kts"), kotlinScriptFixture)

	runIndexIn(t, ".", indexOpts{skipCalibration: true})

	type row struct {
		typ        semantic.ChunkType
		start, end int
	}
	stored := storedChunks(t)
	for file, want := range map[string]map[string]row{
		"Shapes.kt": {
			"Shape/interface/3":    {semantic.ChunkInterface, 3, 5},
			"area/method/4":        {semantic.ChunkMethod, 4, 4},
			"Circle/struct/7":      {semantic.ChunkStruct, 7, 15},
			"area/method/8":        {semantic.ChunkMethod, 8, 10},
			"Companion/struct/12":  {semantic.ChunkStruct, 12, 14},
			"unit/method/13":       {semantic.ChunkMethod, 13, 13},
			"Registry/struct/17":   {semantic.ChunkStruct, 17, 19},
			"register/method/18":   {semantic.ChunkMethod, 18, 18},
			"describe/function/21": {semantic.ChunkFunction, 21, 23},
		},
		"build.gradle.kts": {
			"hello/function/3": {semantic.ChunkFunction, 3, 3},
		},
	} {
		got := map[string]row{}
		for _, c := range stored[file] {
			if c.Language != "kotlin" {
				t.Errorf("%s row %q has language %q, want kotlin", file, c.Name, c.Language)
			}
			got[c.Name+"/"+c.Type.String()+"/"+strconv.Itoa(c.StartLine)] = row{c.Type, c.StartLine, c.EndLine}
		}
		for k, w := range want {
			if g, ok := got[k]; !ok {
				t.Errorf("%s: no stored row %q; have %v", file, k, got)
			} else if g != w {
				t.Errorf("%s: row %q = %+v, want %+v", file, k, g, w)
			}
		}
		if len(got) != len(want) {
			t.Errorf("%s: %d stored rows, want %d: %v", file, len(got), len(want), got)
		}
	}
}
