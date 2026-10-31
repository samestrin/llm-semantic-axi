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

// parseReadmeLanguageTable returns extension -> call-edges cell from the
// "## Languages" table in README.md.
func parseReadmeLanguageTable(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	_, section, ok := strings.Cut(string(data), "\n## Languages\n")
	if !ok {
		t.Fatal("README.md has no \"## Languages\" section")
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
				t.Errorf("README lists .%s twice", m[1])
			}
			got[m[1]] = strings.TrimSpace(cells[3])
		}
	}
	return got
}

// Defends against the README language table drifting from RegisterAllChunkers:
// an extension registered without a row (or a row for one that is not
// registered) fails, and only Go may claim call edges.
func TestReadmeLanguageTable_MatchesRegisteredExtensions(t *testing.T) {
	factory := semantic.NewChunkerFactory()
	RegisterAllChunkers(factory)
	registered := map[string]bool{}
	for _, e := range factory.SupportedExtensions() {
		registered[e] = true
	}

	table := parseReadmeLanguageTable(t)

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
		t.Errorf("registered but missing from README language table: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("in README language table but not registered: %v", extra)
	}

	for e, edges := range table {
		want := "no"
		if e == "go" {
			want = "yes"
		}
		if edges != want {
			t.Errorf(".%s call edges = %q in README, want %q", e, edges, want)
		}
	}
}
