package commands

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// readPublicDoc reads a file from the repository root, failing the test when it
// is missing: a reference page that was never written must not read as a page
// that passed its checks.
func readPublicDoc(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// publicDocSection returns the body of the first "## <heading>" section whose
// heading starts with the given text, up to the next "## " heading.
func publicDocSection(t *testing.T, doc, heading, name string) string {
	t.Helper()
	lines := strings.Split(doc, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "## "+heading) {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s has no %q section", name, "## "+heading)
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// paragraphStartingWith returns the blank-line-separated paragraph that begins
// with prefix, or "" when there is none.
func paragraphStartingWith(doc, prefix string) string {
	for _, para := range strings.Split(doc, "\n\n") {
		para = strings.TrimSpace(para)
		if strings.HasPrefix(para, prefix) {
			return para
		}
	}
	return ""
}

// TestLanguagesDocKeepsCallEdgeLimits defends the move of the call-edge reference
// out of README.md into docs/languages.md. The rule this repository exists to
// keep is that an empty callers/refs answer in a language with no recorded edges
// means "not indexed", never "none exist". A move that dropped the bold rule, a
// language's "not proof" sentence, the imports caveat or the rebuild warning
// would make an empty answer read as proof, so each is checked here.
func TestLanguagesDocKeepsCallEdgeLimits(t *testing.T) {
	page := readPublicDoc(t, "docs/languages.md")

	// The rule stays prominent: the line carrying it must still be emphasised.
	rule := ""
	for _, line := range strings.Split(page, "\n") {
		if strings.Contains(line, `not "no callers."`) {
			rule = line
			break
		}
	}
	if rule == "" {
		t.Fatal(`docs/languages.md lost the rule that an empty callers result means "not indexed for edges", not "no callers."`)
	}
	if !strings.Contains(rule, "not indexed for edges") {
		t.Errorf("docs/languages.md rule no longer names the other half of the distinction: %s", rule)
	}
	if !strings.Contains(rule, "**") {
		t.Errorf("docs/languages.md rule is no longer emphasised, so an empty answer reads as proof: %s", rule)
	}
	if !strings.Contains(page, "Do not read it as proof.") {
		t.Error(`docs/languages.md lost "Do not read it as proof."`)
	}

	// Every edge-recording language keeps its own limits and its own "not proof".
	for _, prefix := range []string{
		"JavaScript and TypeScript record a call",
		"Python records a call",
		"Rust records a call",
	} {
		para := paragraphStartingWith(page, prefix)
		if para == "" {
			t.Errorf("docs/languages.md lost the paragraph starting %q", prefix)
			continue
		}
		if !strings.Contains(para, "not proof") {
			t.Errorf("docs/languages.md paragraph %q no longer says an empty answer is not proof", prefix)
		}
	}

	// An imports edge names a module, which has no chunk: never a resolved link.
	if !strings.Contains(page, "`refs` always lists it as unresolved") {
		t.Error("docs/languages.md lost the rule that an imports edge is always unresolved")
	}

	// An index built before the edges existed has none, and index-update will not
	// add them: the rebuild warning has to survive the move.
	if !strings.Contains(page, "llm-semantic index --force") {
		t.Error("docs/languages.md lost the index --force rebuild warning")
	}
	if !strings.Contains(page, "does not re-chunk files whose hash is unchanged") {
		t.Error("docs/languages.md lost the note that index-update does not re-chunk unchanged files")
	}

	// The three cases an empty answer can be, which the commands report.
	for _, want := range []string{"symbol not indexed", "not indexed for edges", "recorded in the call graph"} {
		if !strings.Contains(page, want) {
			t.Errorf("docs/languages.md lost the empty-answer case %q", want)
		}
	}
}

// TestReadmeDocumentationSectionLinksReferencePages defends the README side of
// the move: the reference pages must stay reachable from a Documentation
// section, docs/axi-audit.md must stay linked as history, every link must resolve
// to a file that exists, and the front page must still name the call-edge
// distinction rather than only pointing at a page that does.
func TestReadmeDocumentationSectionLinksReferencePages(t *testing.T) {
	readme := readPublicDoc(t, "README.md")
	section := publicDocSection(t, readme, "Documentation", "README.md")

	linkRe := regexp.MustCompile(`\(([^)\s]+\.md)\)`)
	linked := map[string]bool{}
	for _, m := range linkRe.FindAllStringSubmatch(section, -1) {
		linked[m[1]] = true
	}
	for _, want := range []string{
		"docs/languages.md",
		"docs/staleness.md",
		"docs/axi-conformance.md",
		"docs/storage-contract.md",
		"docs/axi-audit.md",
	} {
		if !linked[want] {
			t.Errorf("README.md Documentation section does not link %s", want)
		}
	}
	for target := range linked {
		if strings.HasPrefix(target, "http") {
			continue
		}
		if _, err := os.Stat(filepath.Join("..", "..", "..", target)); err != nil {
			t.Errorf("README.md Documentation section links %s, which does not exist: %v", target, err)
		}
	}

	// The audit is a snapshot, not the current record: its line must say so.
	auditLine := ""
	for _, line := range strings.Split(section, "\n") {
		if strings.Contains(line, "docs/axi-audit.md") {
			auditLine = line
			break
		}
	}
	if auditLine == "" {
		t.Fatal("README.md Documentation section has no line for docs/axi-audit.md")
	}
	if !strings.Contains(strings.ToLower(auditLine), "history") {
		t.Errorf("README.md links docs/axi-audit.md without saying it is history: %s", auditLine)
	}

	// The sharpest limit stays visible on the front page, not only in docs/.
	if !strings.Contains(readme, "not indexed for edges") {
		t.Error(`README.md no longer names "not indexed for edges", so an empty callers answer reads as "none exist"`)
	}
}

// TestAxiConformanceDocKeepsTenPrinciples defends the move of the conformance
// table into docs/axi-conformance.md: all ten principles must arrive, in order,
// each with a verdict and a note. A truncated move that dropped a row would
// silently drop an exception, which is the same failure as reporting success for
// work that did not happen.
func TestAxiConformanceDocKeepsTenPrinciples(t *testing.T) {
	page := readPublicDoc(t, "docs/axi-conformance.md")

	var rows [][]string
	for _, line := range strings.Split(page, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) != 4 {
			continue
		}
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if _, err := strconv.Atoi(cells[0]); err != nil {
			continue // header or separator row
		}
		rows = append(rows, cells)
	}
	if len(rows) != 10 {
		t.Fatalf("docs/axi-conformance.md has %d numbered principle rows, want 10", len(rows))
	}
	for i, cells := range rows {
		if cells[0] != strconv.Itoa(i+1) {
			t.Errorf("docs/axi-conformance.md row %d is numbered %q, want %d", i+1, cells[0], i+1)
		}
		if cells[1] == "" {
			t.Errorf("docs/axi-conformance.md principle %s has no name", cells[0])
		}
		status := strings.Trim(cells[2], "*")
		if status != "Met" && status != "Exception" {
			t.Errorf("docs/axi-conformance.md principle %s status = %q, want Met or Exception", cells[0], cells[2])
		}
		if cells[3] == "" {
			t.Errorf("docs/axi-conformance.md principle %s has no note, so its verdict is unexplained", cells[0])
		}
	}

	// The standing --json exception is part of the record, not a footnote that
	// can fall off in a move.
	if !strings.Contains(page, "`--json` keeps its pre-AXI schema") {
		t.Error("docs/axi-conformance.md lost the --json scope note")
	}
	if !strings.Contains(page, "standing exception") {
		t.Error("docs/axi-conformance.md no longer calls the --json schema a standing exception")
	}
}

// TestStalenessDocKeepsHonestReportingRules defends the move of the staleness
// reference into docs/staleness.md. The parts that keep a stale or unmeasured
// index from reading as a fresh one -- counts only when measured, the plain line
// on stderr for --json/--min, and a record written only when every file indexed
// -- must survive the move.
func TestStalenessDocKeepsHonestReportingRules(t *testing.T) {
	page := readPublicDoc(t, "docs/staleness.md")

	for _, want := range []string{
		"Counts appear only when measured",
		"stderr",
		"every file indexed",
		"leaves the previous record in place",
		"not_applicable",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("docs/staleness.md lost %q", want)
		}
	}
	for _, state := range []string{"fresh", "stale", "unknown"} {
		if !strings.Contains(page, "`"+state+"`") {
			t.Errorf("docs/staleness.md lost the %q state", state)
		}
	}
}
