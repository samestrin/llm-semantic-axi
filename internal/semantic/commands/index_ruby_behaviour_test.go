package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunIndexStoresRubyClassesModulesAndMethodsWithTheirLines is epic 02.1
// T1's "done when", checked in the database rather than by exit code. Before
// the Ruby chunker `.rb` had no chunker, so `index` skipped the file and the
// index held no row for it at all.
func TestRunIndexStoresRubyClassesModulesAndMethodsWithTheirLines(t *testing.T) {
	// Read the treesitter package's fixture before newWorkspace changes the
	// working directory, so this test and the chunker test pin the same file.
	sample, err := os.ReadFile(filepath.Join("..", "treesitter", "testdata", "ruby", "sample.rb"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.rb"), string(sample))

	out := runIndexIn(t, ".", indexOpts{skipCalibration: true})
	if strings.Contains(out, "text windows") {
		t.Errorf("a clean Ruby file fell back to text windows:\n%s", out)
	}

	want := []string{
		"== method 44-46",
		"Billing file 3-41",
		"Billing::Refund struct 43-47",
		"Formatting file 36-40",
		"Invoice struct 6-34",
		"audit method 31-33",
		"build method 21-23",
		"format_amount method 37-39",
		"from_json method 26-28",
		"helper function 49-49",
		"initialize method 9-11",
		"main function 51-53",
		"paid? method 17-19",
		"total method 13-15",
	}
	if got := storedRows(storedChunks(t)["sample.rb"]); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("sample.rb rows:\n  got  %v\n  want %v", got, want)
	}
}
