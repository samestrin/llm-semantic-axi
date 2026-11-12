package benchmarks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readmePath is the page the Performance table is copied into. The guard
// reads it the way internal/semantic/commands/readme_table_test.go reads its
// page: with a relative path out of the package directory.
const readmePath = "../README.md"

// sectionBody returns the body of a markdown section, from its heading line to
// the next "## " heading (or the end of the file).
func sectionBody(t *testing.T, doc, heading string) string {
	t.Helper()
	marker := heading + "\n"
	start := strings.Index(doc, "\n"+marker)
	if start < 0 {
		if strings.HasPrefix(doc, marker) {
			start = -1
		} else {
			t.Fatalf("no %q section", heading)
		}
	}
	body := doc[start+len(marker):]
	if k := strings.Index(body, "\n## "); k >= 0 {
		body = body[:k]
	}
	return body
}

// tableRows returns every markdown table row in a section, in order.
func tableRows(section string) []string {
	var rows []string
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "|") {
			rows = append(rows, line)
		}
	}
	return rows
}

// Defends the copied Performance table: a figure typed into README.md by hand
// is a number no benchmark produced. Every row must match the generated
// benchmarks/results-performance.md row for row, so changing one figure fails
// here instead of shipping as measured.
func TestReadmePerformanceTableMatchesTheGeneratedResults(t *testing.T) {
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read %s: %v", readmePath, err)
	}
	got := tableRows(sectionBody(t, string(readme), "## Performance"))
	if len(got) == 0 {
		t.Fatalf("%s has no Performance table", readmePath)
	}

	generated, err := os.ReadFile(PerfResultsFile)
	if err != nil {
		t.Fatalf("read %s: %v", PerfResultsFile, err)
	}
	want := tableRows(sectionBody(t, string(generated), "# Performance"))
	if len(got) != len(want) {
		t.Fatalf("%s Performance table has %d rows, %s has %d", readmePath, len(got), PerfResultsFile, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s Performance row %d does not match %s; run `make bench-perf` and copy the table\n want: %s\n  got: %s", readmePath, i+1, PerfResultsFile, want[i], got[i])
		}
	}
}

// Defends the demo embed: the GIF is a file in the repo linked by relative
// path, so the README shows a real session from a checkout and never points at
// a hosted recording.
func TestReadmeDemoEmbedsTheCommittedGIF(t *testing.T) {
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read %s: %v", readmePath, err)
	}
	demo := sectionBody(t, string(readme), "## Demo")
	for _, want := range []string{"](docs/demo/llm-semantic.gif)", "docs/demo/README.md"} {
		if !strings.Contains(demo, want) {
			t.Errorf("Demo section lacks %q:\n%s", want, demo)
		}
	}
	if strings.Contains(demo, "vhs.charm.sh") {
		t.Errorf("Demo section links a hosted recording:\n%s", demo)
	}
	if _, err := os.Stat(filepath.Join("..", "docs", "demo", "llm-semantic.gif")); err != nil {
		t.Errorf("committed GIF missing: %v", err)
	}
}
