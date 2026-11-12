package commands

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// committedSkillRel is the installable skill in the repository, at the
// `skills/<name>/SKILL.md` path the `skills` CLI discovers for
// `npx skills add samestrin/llm-semantic-axi --skill llm-semantic`.
const committedSkillRel = "skills/llm-semantic/SKILL.md"

// readCommittedSkill reads the skill file from the repository, not from a
// project setup wrote into.
func readCommittedSkill(t *testing.T) (path string, content []byte) {
	t.Helper()
	path = filepath.Join("..", "..", "..", committedSkillRel)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the committed skill %s: %v", committedSkillRel, err)
	}
	return path, content
}

// setupWrittenSkill runs setup in a fresh project and returns the bytes it
// actually wrote, so the comparison is against a real side effect rather than
// against the constant the code happens to hold.
func setupWrittenSkill(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	if err := runSetup(&bytes.Buffer{}, dir, false); err != nil {
		t.Fatalf("runSetup: %v", err)
	}
	p := filepath.Join(dir, ".claude", "skills", "llm-semantic", "SKILL.md")
	content, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("setup did not write %s: %v", p, err)
	}
	return content
}

// skillDiff names the first line where two skill texts differ, so a drift
// failure points at the line instead of dumping two near-identical documents.
func skillDiff(committed, written []byte) string {
	cl, wl := strings.Split(string(committed), "\n"), strings.Split(string(written), "\n")
	for i := 0; i < len(cl) || i < len(wl); i++ {
		var c, w string
		if i < len(cl) {
			c = cl[i]
		}
		if i < len(wl) {
			w = wl[i]
		}
		if c != w {
			return fmt.Sprintf("line %d:\n  committed: %q\n  setup:     %q", i+1, c, w)
		}
	}
	return fmt.Sprintf("no differing line; committed is %d bytes, setup wrote %d", len(committed), len(written))
}

// TestCommittedSkillEqualsWhatSetupWrites defends the single promise of the
// installable skill: someone who installs it with `npx skills add` and someone
// who runs `llm-semantic setup` must read the same instructions. Two copies
// drift silently, and the copy that drifts is the one no test reads.
func TestCommittedSkillEqualsWhatSetupWrites(t *testing.T) {
	_, committed := readCommittedSkill(t)
	written := setupWrittenSkill(t)

	if !bytes.Equal(committed, written) {
		t.Errorf("%s differs from the skill setup writes\n%s", committedSkillRel, skillDiff(committed, written))
	}
}

// splitSkillFrontmatter splits a SKILL.md into its YAML header and its body.
// Both delimiters must be whole lines: a header that is never closed is not
// frontmatter, and a loader would read the instructions as part of it.
func splitSkillFrontmatter(t *testing.T, text string) (header, body string) {
	t.Helper()
	lines := strings.Split(text, "\n")
	if lines[0] != "---" {
		t.Fatalf("SKILL.md does not open with a \"---\" line:\n%s", text)
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return strings.Join(lines[1:i], "\n"), strings.Join(lines[i+1:], "\n")
		}
	}
	t.Fatalf("SKILL.md frontmatter is never closed by a \"---\" line:\n%s", text)
	return "", ""
}

// TestCommittedSkillFrontmatterIsValid defends installability: the `skills` CLI
// and Claude Code read the YAML header to decide what the skill is called and
// when to use it. A header that does not parse, a name that does not match the
// directory it sits in, or an empty description makes the installed skill
// unusable while the file still looks present.
func TestCommittedSkillFrontmatterIsValid(t *testing.T) {
	path, content := readCommittedSkill(t)
	header, body := splitSkillFrontmatter(t, string(content))

	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(header), &fm); err != nil {
		t.Fatalf("frontmatter is not valid YAML: %v\n%s", err, header)
	}

	dirName := filepath.Base(filepath.Dir(path))
	if fm.Name != dirName {
		t.Errorf("frontmatter name = %q, want the directory name %q", fm.Name, dirName)
	}
	if fm.Name != "llm-semantic" {
		t.Errorf("frontmatter name = %q, want \"llm-semantic\"", fm.Name)
	}
	// Agent Skills name rules: lower-case letters, digits and hyphens, 64 at most.
	if !regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`).MatchString(fm.Name) || len(fm.Name) > 64 {
		t.Errorf("frontmatter name %q is not a valid skill name", fm.Name)
	}
	if strings.TrimSpace(fm.Description) == "" {
		t.Error("frontmatter has no description; a skill with none is never selected")
	}
	if len(fm.Description) > 1024 {
		t.Errorf("frontmatter description is %d characters, over the 1024 limit", len(fm.Description))
	}
	if strings.TrimSpace(body) == "" {
		t.Error("SKILL.md has frontmatter but no instructions")
	}
}

// TestSkillTextClaimsEveryCallEdgeLanguage defends the claim that made this
// task necessary: `callers` and `refs` answer for Go, JavaScript, TypeScript,
// Python and Rust, and the skill told an agent they work "for Go only" and
// described "a Go symbol". An agent reading that stops asking about the other
// four languages, which is the same silent wrong answer as an empty result
// reported as "none exist". Both copies are checked, so neither the committed
// file nor the text setup writes can carry the stale claim on its own.
func TestSkillTextClaimsEveryCallEdgeLanguage(t *testing.T) {
	_, committed := readCommittedSkill(t)
	for _, copy := range []struct {
		label string
		text  string
	}{
		{committedSkillRel, string(committed)},
		{"setupSkillContent", setupSkillContent},
	} {
		lower := strings.ToLower(copy.text)
		for _, stale := range []string{"go only", "a go symbol"} {
			if strings.Contains(lower, stale) {
				t.Errorf("%s still claims %q", copy.label, stale)
			}
		}

		var edgesLine string
		for _, line := range strings.Split(copy.text, "\n") {
			if strings.Contains(line, "`llm-semantic callers") {
				edgesLine = line
				break
			}
		}
		if edgesLine == "" {
			t.Errorf("%s has no line documenting `llm-semantic callers`", copy.label)
			continue
		}
		for _, lang := range []string{"Go", "JavaScript", "TypeScript", "Python", "Rust"} {
			if !strings.Contains(edgesLine, lang) {
				t.Errorf("%s: the callers/refs line does not name %s:\n%s", copy.label, lang, edgesLine)
			}
		}
		// The other half of the rule: an empty result elsewhere is a gap in the
		// index, not proof that no caller exists.
		if !strings.Contains(edgesLine, "means not indexed") {
			t.Errorf("%s: the callers/refs line lost the \"means not indexed\" wording:\n%s", copy.label, edgesLine)
		}
	}
}
