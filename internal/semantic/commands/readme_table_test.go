package commands

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// languageTablePath is the page the language table lives on. It moved out of
// README.md into docs/languages.md; the guard followed it, so the table cannot
// drift from RegisterAllChunkers wherever it is published.
const languageTablePath = "docs/languages.md"

// parseLanguageTable returns extension -> call-edges cell from the
// "## Language table" table in docs/languages.md.
func parseLanguageTable(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", languageTablePath))
	if err != nil {
		t.Fatalf("read %s: %v", languageTablePath, err)
	}
	_, section, ok := strings.Cut(string(data), "\n## Language table\n")
	if !ok {
		t.Fatalf("%s has no \"## Language table\" section", languageTablePath)
	}
	if k := strings.Index(section, "\n## "); k >= 0 {
		section = section[:k]
	}
	extRe := regexp.MustCompile("`\\.([A-Za-z0-9]+)`")
	got := map[string]string{}
	for _, line := range strings.Split(section, "\n") {
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) != 4 {
			continue
		}
		for _, m := range extRe.FindAllStringSubmatch(cells[1], -1) {
			if _, dup := got[m[1]]; dup {
				t.Errorf("%s lists .%s twice", languageTablePath, m[1])
			}
			got[m[1]] = strings.TrimSpace(cells[3])
		}
	}
	return got
}

// Defends against the published language table drifting from
// RegisterAllChunkers: an extension registered without a row (or a row for one
// that is not registered) fails, and only Go, JavaScript, TypeScript, Python and
// Rust may claim call edges. The table now lives in docs/languages.md; the test
// keeps its name so the guard is findable by the name the docs and changelog
// already cite.
func TestReadmeLanguageTable_MatchesRegisteredExtensions(t *testing.T) {
	factory := semantic.NewChunkerFactory()
	RegisterAllChunkers(factory)
	registered := map[string]bool{}
	for _, e := range factory.SupportedExtensions() {
		registered[e] = true
	}

	table := parseLanguageTable(t)

	var missing, extra []string
	for e := range registered {
		if _, ok := table[e]; !ok {
			missing = append(missing, e)
		}
	}
	for e := range table {
		if !registered[e] {
			extra = append(extra, e)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("registered but missing from %s language table: %v", languageTablePath, missing)
	}
	if len(extra) > 0 {
		t.Errorf("in %s language table but not registered: %v", languageTablePath, extra)
	}

	edgeExts := map[string]bool{"go": true, "js": true, "jsx": true, "mjs": true, "cjs": true,
		"py": true, "pyi": true, "pyw": true, "ts": true, "tsx": true, "mts": true, "cts": true,
		"rs": true}
	for e, edges := range table {
		want := "no"
		if edgeExts[e] {
			want = "yes"
		}
		if edges != want {
			t.Errorf(".%s call edges = %q in %s, want %q", e, edges, languageTablePath, want)
		}
	}
}
