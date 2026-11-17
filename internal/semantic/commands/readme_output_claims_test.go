package commands

import (
	"strconv"
	"strings"
	"testing"
)

// These tests pin the README and conformance-doc claims EST-6829 corrected,
// so the wording cannot drift back to a shape the binary does not print.

// TestReadmeSaysAtMostFourFieldsPerResult defends finding 1's doc side: the
// default TOON row of callers carries three fields and refs four, so the
// README's blanket "four fields per result" was an overclaim. The claim is
// now "at most four", which every command's default row satisfies.
func TestReadmeSaysAtMostFourFieldsPerResult(t *testing.T) {
	readme := readPublicDoc(t, "README.md")

	if !strings.Contains(readme, "at most four fields per result") {
		t.Error("README.md no longer says the default row carries at most four fields per result")
	}
	if strings.Contains(readme, "with four fields per result") {
		t.Error("README.md still claims exactly four fields per result; callers and refs default rows carry fewer")
	}
}

// TestReadmeEmptyJSONWordingMatchesBehaviour defends finding 2: the old line
// said [] appears only "when reranking is on", but --hybrid fusion and --min
// also print [] when empty, and a dense --threshold search prints null. The
// wording must name the true conditions and keep "treat both as empty".
func TestReadmeEmptyJSONWordingMatchesBehaviour(t *testing.T) {
	readme := readPublicDoc(t, "README.md")

	for _, want := range []string{
		"`null` when the store returns nothing",
		"`[]` when reranking or `--hybrid` fusion removes every result",
		"treat both as empty",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md lost the verified zero-result wording %q", want)
		}
	}
	if strings.Contains(readme, "or `[]` when reranking is on") {
		t.Error("README.md still says [] appears only when reranking is on")
	}
}

// TestAxiConformanceCoversCallersAndRefs defends finding 1's conformance side:
// rows 2 (minimal schemas) and 4 (stated totals) must name callers and refs,
// not only the search family, now that their default TOON output conforms.
func TestAxiConformanceCoversCallersAndRefs(t *testing.T) {
	page := readPublicDoc(t, "docs/axi-conformance.md")

	rows := map[int]string{}
	for _, line := range strings.Split(page, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) != 4 {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(cells[0]))
		if err != nil {
			continue
		}
		rows[n] = cells[3]
	}
	for _, n := range []int{2, 4} {
		note := rows[n]
		if note == "" {
			t.Fatalf("docs/axi-conformance.md has no row %d", n)
		}
		if !strings.Contains(note, "callers") || !strings.Contains(note, "refs") {
			t.Errorf("row %d does not cover callers and refs: %s", n, note)
		}
	}
	if !strings.Contains(rows[4], "total") {
		t.Errorf("row 4 does not state the total callers and refs carry: %s", rows[4])
	}
}
