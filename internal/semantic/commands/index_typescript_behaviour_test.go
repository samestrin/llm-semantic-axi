package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// readTSFixture reads a fixture from the treesitter package's testdata, so
// this test and the chunker test pin the same file. Call it before
// newWorkspace changes the working directory.
func readTSFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "treesitter", "testdata", "typescript", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func storedRows(chunks []semantic.Chunk) []string {
	rows := make([]string, 0, len(chunks))
	for _, c := range chunks {
		rows = append(rows, fmt.Sprintf("%s %v %d-%d", c.Name, c.Type, c.StartLine, c.EndLine))
	}
	sort.Strings(rows)
	return rows
}

// TestRunIndexStoresTypeScriptAndTSXDeclarationsWithTheirLines is task T3's
// "done when", checked in the database rather than by exit code. Before
// TypeScriptChunker, a .tsx component parsed with the plain TypeScript
// grammar fell back to text windows, and .mts/.cts files were not indexed at
// all.
func TestRunIndexStoresTypeScriptAndTSXDeclarationsWithTheirLines(t *testing.T) {
	sample := readTSFixture(t, "sample.ts")
	component := readTSFixture(t, "component.tsx")

	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.ts"), sample)
	writeFile(t, filepath.Join(dir, "component.tsx"), component)
	writeFile(t, filepath.Join(dir, "esm.mts"), "export function loadConfig<T>(path: string): T {\n    return JSON.parse(path) as T;\n}\n")
	writeFile(t, filepath.Join(dir, "cjs.cts"), "interface Options {\n    verbose: boolean;\n}\n")

	out := runIndexIn(t, ".", indexOpts{skipCalibration: true})
	if strings.Contains(out, "text windows") {
		t.Errorf("clean TypeScript files fell back to text windows:\n%s", out)
	}

	byFile := storedChunks(t)

	// Every construct in the .tsx fixture, including the JSX components.
	wantTSX := []string{
		"Button function 10-16",
		"ButtonProps interface 3-6",
		"Counter function 18-21",
		"List function 23-25",
		"Panel struct 27-31",
		"Variant struct 8-8",
		"render method 28-30",
	}
	if got := storedRows(byFile["component.tsx"]); strings.Join(got, "\n") != strings.Join(wantTSX, "\n") {
		t.Errorf("component.tsx rows:\n  got  %v\n  want %v", got, wantTSX)
	}

	// Interfaces, types, classes and generics in the .ts fixture.
	tsRows := map[string]bool{}
	for _, r := range storedRows(byFile["sample.ts"]) {
		tsRows[r] = true
	}
	for _, want := range []string{
		"UserDTO interface 3-7",
		"UserRole struct 9-9",
		"UserController struct 11-27",
		"getUser method 18-21",
		"Repository interface 42-45",
		"Result struct 47-47",
		"Box struct 49-55",
		"map method 52-54",
		"identity function 57-59",
		"Shape struct 61-67",
		"Color struct 69-72",
	} {
		if !tsRows[want] {
			t.Errorf("sample.ts has no row %q; rows: %v", want, storedRows(byFile["sample.ts"]))
		}
	}

	if got := storedRows(byFile["esm.mts"]); strings.Join(got, ",") != "loadConfig function 1-3" {
		t.Errorf("esm.mts rows = %v, want [loadConfig function 1-3]", got)
	}
	if got := storedRows(byFile["cjs.cts"]); strings.Join(got, ",") != "Options interface 1-3" {
		t.Errorf("cjs.cts rows = %v, want [Options interface 1-3]", got)
	}
}
