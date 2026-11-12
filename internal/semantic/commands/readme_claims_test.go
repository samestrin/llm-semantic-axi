package commands

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// readmeSectionOrder is the README standard's (v2) list of top-level sections,
// in order. A required section must be present; an optional one may be left
// out but, when present, must sit in its slot. Demo and Performance are
// optional here because a later change adds them.
var readmeSectionOrder = []struct {
	heading  string
	required bool
}{
	{"Demo", false},
	{"Why", true},
	{"Install", true},
	{"Quick start", true},
	{"Usage", true},
	{"Configuration", true},
	{"Output and exit codes", true},
	{"Performance", false},
	{"How it works", false},
	{"Limits", true},
	{"Documentation", true},
	{"Development", true},
	{"Changelog", true},
	{"License", true},
}

// externalFlags are long flags the README shows on another tool's command
// line, so they cannot exist in this repository. Each names its tool.
var externalFlags = map[string]string{
	"--skill": "npx skills add",
}

// readmeProse returns the README with fenced code blocks blanked out, line for
// line, so prose checks see only prose and line numbers still match.
func readmeProse(readme string) []string {
	lines := strings.Split(readme, "\n")
	out := make([]string, len(lines))
	inFence := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			out[i] = line
		}
	}
	return out
}

// readmeCodeBlocks returns the body of every fenced code block in a section.
func readmeCodeBlocks(section string) [][]string {
	var blocks [][]string
	var cur []string
	inFence := false
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inFence {
				blocks = append(blocks, cur)
				cur = nil
			}
			inFence = !inFence
			continue
		}
		if inFence {
			cur = append(cur, line)
		}
	}
	return blocks
}

// TestReadmeFollowsStandardSections defends the README's layout against
// drifting from the README standard: the title and one-sentence opener, a
// single badge line of at most four badges, every required section present and
// in order, no section outside the list, three to five Why bullets, and a
// Quick start of at most six commands followed by its output. A reader (and a
// catalog reviewer) relies on finding Install, exit codes and Limits where the
// standard puts them.
func TestReadmeFollowsStandardSections(t *testing.T) {
	readme := readPublicDoc(t, "README.md")
	prose := readmeProse(readme)

	if prose[0] != "# llm-semantic" {
		t.Errorf("README.md starts %q, want the title \"# llm-semantic\"", prose[0])
	}
	var head []string
	for _, line := range prose[1:] {
		if strings.HasPrefix(line, "## ") {
			break
		}
		if strings.TrimSpace(line) != "" {
			head = append(head, line)
		}
	}
	if len(head) == 0 {
		t.Fatal("README.md has nothing between the title and the first section")
	}
	opener := head[0]
	if strings.HasPrefix(opener, ">") || strings.Contains(opener, "![") {
		t.Errorf("README.md opener must be a sentence, not a tagline or a badge: %q", opener)
	}
	if n := strings.Count(strings.TrimSuffix(opener, "."), ". "); n != 0 || !strings.HasSuffix(opener, ".") {
		t.Errorf("README.md opener must be one sentence: %q", opener)
	}
	badgeLines := 0
	for _, line := range head {
		if n := strings.Count(line, "!["); n > 0 {
			badgeLines++
			if n > 4 {
				t.Errorf("README.md badge line has %d badges, want at most 4: %s", n, line)
			}
		}
	}
	if badgeLines != 1 {
		t.Errorf("README.md has %d badge lines, want exactly one", badgeLines)
	}

	var got []string
	for _, line := range prose {
		if strings.HasPrefix(line, "## ") {
			got = append(got, strings.TrimPrefix(line, "## "))
		}
	}
	slot := map[string]int{}
	for i, s := range readmeSectionOrder {
		slot[s.heading] = i
	}
	last := -1
	present := map[string]bool{}
	for _, h := range got {
		i, ok := slot[h]
		if !ok {
			t.Errorf("README.md has a section the standard does not list: ## %s", h)
			continue
		}
		if present[h] {
			t.Errorf("README.md has ## %s twice", h)
		}
		present[h] = true
		if i < last {
			t.Errorf("README.md ## %s is out of order (sections: %v)", h, got)
		}
		last = i
	}
	for _, s := range readmeSectionOrder {
		if s.required && !present[s.heading] {
			t.Errorf("README.md is missing the required section ## %s", s.heading)
		}
	}
	if t.Failed() {
		return
	}

	bullets := 0
	for _, line := range strings.Split(publicDocSection(t, readme, "Why", "README.md"), "\n") {
		if strings.HasPrefix(line, "- ") {
			bullets++
		}
	}
	if bullets < 3 || bullets > 5 {
		t.Errorf("README.md ## Why has %d bullets, want three to five", bullets)
	}

	blocks := readmeCodeBlocks(publicDocSection(t, readme, "Quick start", "README.md"))
	if len(blocks) < 2 {
		t.Fatalf("README.md ## Quick start has %d code blocks, want the commands and then their output", len(blocks))
	}
	commands := 0
	for _, line := range blocks[0] {
		if s := strings.TrimSpace(line); s != "" && !strings.HasPrefix(s, "#") {
			commands++
		}
	}
	if commands == 0 || commands > 6 {
		t.Errorf("README.md ## Quick start has %d commands, want one to six", commands)
	}
}

// commandTreeFlags returns every long flag any command in the llm-semantic tree
// accepts, local, persistent or inherited.
func commandTreeFlags() map[string]bool {
	flags := map[string]bool{"--help": true}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		add := func(f *pflag.Flag) { flags["--"+f.Name] = true }
		c.Flags().VisitAll(add)
		c.PersistentFlags().VisitAll(add)
		c.InheritedFlags().VisitAll(add)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(RootCmd())
	return flags
}

// sourceFlags returns the long flags this repository accepts outside the cobra
// tree: the installer's options and the flags the two mains read themselves
// (the root --version).
func sourceFlags(t *testing.T) map[string]bool {
	t.Helper()
	flags := map[string]bool{}
	installer := readPublicDoc(t, "install-release.sh")
	for _, m := range regexp.MustCompile(`(?m)^\s*(?:-[a-zA-Z] \| )?(--[a-z][a-z-]*)\)`).FindAllStringSubmatch(installer, -1) {
		flags[m[1]] = true
	}
	for _, main := range []string{"cmd/llm-semantic/main.go", "cmd/llm-semantic-mcp/main.go"} {
		for _, m := range regexp.MustCompile(`"(--[a-z][a-z-]*)"`).FindAllStringSubmatch(readPublicDoc(t, main), -1) {
			flags[m[1]] = true
		}
	}
	return flags
}

// readmeLongFlags returns each long flag the README names, prose and code
// blocks alike, with the first line it appears on.
func readmeLongFlags(readme string) map[string]int {
	re := regexp.MustCompile(`(?:^|[\s\x60(\[])(--[a-z][a-z0-9-]*)`)
	found := map[string]int{}
	for i, line := range strings.Split(readme, "\n") {
		for _, m := range re.FindAllStringSubmatch(line, -1) {
			if _, ok := found[m[1]]; !ok {
				found[m[1]] = i + 1
			}
		}
	}
	return found
}

// TestReadmeFlagsExist defends the README against naming a flag the tool does
// not have. A reader who copies `--foo` from the front page and gets "unknown
// flag" has been told something that is not true. Every long flag must exist in
// the command tree, in the installer, or in a main that reads it itself.
func TestReadmeFlagsExist(t *testing.T) {
	readme := readPublicDoc(t, "README.md")
	known := commandTreeFlags()
	for f := range sourceFlags(t) {
		known[f] = true
	}
	named := readmeLongFlags(readme)
	if len(named) == 0 {
		t.Fatal("README.md names no flags; the extraction is broken")
	}
	var names []string
	for f := range named {
		names = append(names, f)
	}
	sort.Strings(names)
	for _, f := range names {
		if known[f] {
			continue
		}
		if _, ok := externalFlags[f]; ok {
			continue
		}
		t.Errorf("README.md:%d names %s, which no command, installer option or main accepts", named[f], f)
	}
}

// TestReadmeFlagCheckRejectsAnUnknownFlag proves the flag guard can fail: a
// README naming a flag nothing accepts must be reported.
func TestReadmeFlagCheckRejectsAnUnknownFlag(t *testing.T) {
	known := commandTreeFlags()
	named := readmeLongFlags("Run `llm-semantic search --no-such-flag`.\n")
	if _, ok := named["--no-such-flag"]; !ok {
		t.Fatalf("extraction missed --no-such-flag: %v", named)
	}
	if known["--no-such-flag"] {
		t.Fatal("the command tree claims --no-such-flag exists")
	}
	if !known["--axi"] || !known["--top"] || !known["--dry-run"] {
		t.Error("the command tree walk misses real flags (--axi, --top, --dry-run)")
	}
}

// sourceEnvVars returns every environment variable the repository's own code
// reads: os.Getenv / os.LookupEnv in non-test Go files, and ${NAME} or $NAME in
// the two install scripts.
func sourceEnvVars(t *testing.T) map[string]bool {
	t.Helper()
	root := filepath.Join("..", "..", "..")
	vars := map[string]bool{}
	goRe := regexp.MustCompile(`(?:Getenv|LookupEnv)\("([A-Z][A-Z0-9_]*)"\)`)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "build" || d.Name() == "benchmarks") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range goRe.FindAllStringSubmatch(string(data), -1) {
			vars[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk source: %v", err)
	}
	shRe := regexp.MustCompile(`\$\{?([A-Z][A-Z0-9_]*)`)
	for _, script := range []string{"install-release.sh", "install.sh"} {
		for _, m := range shRe.FindAllStringSubmatch(readPublicDoc(t, script), -1) {
			vars[m[1]] = true
		}
	}
	return vars
}

// readmeEnvVars returns each environment variable the README names: a $NAME
// anywhere, or an upper-case NAME_WITH_UNDERSCORES in backticks or code.
func readmeEnvVars(readme string) map[string]int {
	dollar := regexp.MustCompile(`\$\{?([A-Z][A-Z0-9_]*)`)
	bare := regexp.MustCompile(`\b([A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+)\b`)
	found := map[string]int{}
	for i, line := range strings.Split(readme, "\n") {
		for _, re := range []*regexp.Regexp{dollar, bare} {
			for _, m := range re.FindAllStringSubmatch(line, -1) {
				if _, ok := found[m[1]]; !ok {
					found[m[1]] = i + 1
				}
			}
		}
	}
	return found
}

// TestReadmeEnvVarsExist defends the Configuration table and the Install
// section against naming an environment variable nothing reads. A setting the
// code ignores is a setting that silently does nothing.
func TestReadmeEnvVarsExist(t *testing.T) {
	readme := readPublicDoc(t, "README.md")
	known := sourceEnvVars(t)
	named := readmeEnvVars(readme)
	if len(named) == 0 {
		t.Fatal("README.md names no environment variables; the extraction is broken")
	}
	var names []string
	for v := range named {
		names = append(names, v)
	}
	sort.Strings(names)
	for _, v := range names {
		if !known[v] {
			t.Errorf("README.md:%d names %s, which no source file or install script reads", named[v], v)
		}
	}
}

// TestReadmeEnvVarCheckRejectsAnUnreadVariable proves the variable guard can
// fail: a name no code reads must not be in the known set.
func TestReadmeEnvVarCheckRejectsAnUnreadVariable(t *testing.T) {
	known := sourceEnvVars(t)
	named := readmeEnvVars("Set `LLM_SEMANTIC_NO_SUCH_VAR` and $ALSO_NOT_READ.\n")
	for _, v := range []string{"LLM_SEMANTIC_NO_SUCH_VAR", "ALSO_NOT_READ"} {
		if _, ok := named[v]; !ok {
			t.Errorf("extraction missed %s: %v", v, named)
		}
		if known[v] {
			t.Errorf("the source scan claims %s is read", v)
		}
	}
	for _, v := range []string{"LLM_SEMANTIC_API_URL", "QDRANT_INSECURE", "LLM_SEMANTIC_INSTALL_DIR"} {
		if !known[v] {
			t.Errorf("the source scan misses %s, which the code reads", v)
		}
	}
}

// voiceViolations returns the README standard's voice breaks in prose: "we",
// "our" or "us", an exclamation mark, or an emoji. Inline code spans and image
// syntax are not prose.
func voiceViolations(prose []string) []string {
	code := regexp.MustCompile("`[^`]*`")
	image := regexp.MustCompile(`!\[`)
	pronoun := regexp.MustCompile(`(?i)\b(we|our|ours|us)\b`)
	var out []string
	for i, line := range prose {
		line = code.ReplaceAllString(line, "")
		line = image.ReplaceAllString(line, "[")
		if m := pronoun.FindString(line); m != "" {
			out = append(out, lineRef(i, "the word "+m))
		}
		if strings.Contains(line, "!") {
			out = append(out, lineRef(i, "an exclamation mark"))
		}
		for _, r := range line {
			if isEmoji(r) {
				out = append(out, lineRef(i, "an emoji "+string(r)))
				break
			}
		}
	}
	return out
}

func lineRef(i int, what string) string {
	return fmt.Sprintf("README.md:%d has %s", i+1, what)
}

// isEmoji reports whether r is in a pictographic or symbol block emojis use.
func isEmoji(r rune) bool {
	return (r >= 0x1F000 && r <= 0x1FAFF) || (r >= 0x2600 && r <= 0x27BF) ||
		(r >= 0x2B00 && r <= 0x2BFF) || r == 0xFE0F || r == 0x200D
}

// TestReadmeVoice defends the README standard's voice outside code blocks: no
// "we", "our" or "us", no exclamation mark and no emoji. Internal wording
// (ticket IDs, private hosts) is guarded by TestPublicDocsHaveNoInternalWording.
func TestReadmeVoice(t *testing.T) {
	for _, v := range voiceViolations(readmeProse(readPublicDoc(t, "README.md"))) {
		t.Error(v)
	}
}

// TestReadmeVoiceCheckCatchesEachBreak proves the voice guard can fail on each
// rule, and that code spans and badge images are left alone.
func TestReadmeVoiceCheckCatchesEachBreak(t *testing.T) {
	for _, bad := range []string{"We index the code.", "Our tool.", "Tell us.", "It works!", "Fast \U0001F680"} {
		if len(voiceViolations([]string{bad})) == 0 {
			t.Errorf("voice check passed %q", bad)
		}
	}
	for _, good := range []string{"[![CI](x.svg)](y)", "Run `echo hi!` here.", "Use the user setting."} {
		if v := voiceViolations([]string{good}); len(v) != 0 {
			t.Errorf("voice check flagged %q: %v", good, v)
		}
	}
}
