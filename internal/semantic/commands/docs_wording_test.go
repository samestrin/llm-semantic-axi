package commands

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// publicDocRoots lists the user-facing files and trees the wording guard
// covers, relative to the repository root. skills/ belongs here because a
// SKILL.md is read by every agent that loads the skill: internal wording there
// reaches users exactly as it does in README.md.
var publicDocRoots = []string{"README.md", "CHANGELOG.md", "docs", "skills"}

// forbiddenPublicWording matches the internal wording that must not reach a
// user: ticket IDs, epic labels, board-decision language, pre-05.1 labels and
// private hostnames or addresses.
func forbiddenPublicWording() *regexp.Regexp {
	return regexp.MustCompile(`EST-\d+|board decision|\(epic \d|pre-05\.1|\.lan\b|192\.168\.|estr\.in`)
}

// publicDocFiles returns every regular file under root named by publicDocRoots.
// A root that is missing is a failure, not an empty result: a guard that walks
// nothing must not read as a guard that found nothing.
func publicDocFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	for _, rel := range publicDocRoots {
		p := filepath.Join(root, rel)
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if !info.IsDir() {
			files = append(files, p)
			continue
		}
		err = filepath.WalkDir(p, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				files = append(files, p)
			}
			return err
		})
		if err != nil {
			t.Fatalf("walk %s: %v", p, err)
		}
	}
	return files
}

// wordingViolations reports every line of files that matches
// forbiddenPublicWording, as "<path>:<line> contains <word>".
func wordingViolations(t *testing.T, files []string) []string {
	t.Helper()
	forbidden := forbiddenPublicWording()
	var out []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if m := forbidden.FindString(line); m != "" {
				out = append(out, fmt.Sprintf("%s:%d contains %q", f, i+1, m))
			}
		}
	}
	return out
}

// copyPublicDocTree copies every file under the guarded roots of src into dst,
// so a test can plant wording in a copy and scan the copy through the same
// publicDocRoots the real guard uses.
func copyPublicDocTree(t *testing.T, src, dst string) {
	t.Helper()
	for _, rel := range publicDocRoots {
		srcPath := filepath.Join(src, rel)
		err := filepath.WalkDir(srcPath, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relPath, err := filepath.Rel(src, p)
			if err != nil {
				return err
			}
			dstPath := filepath.Join(dst, relPath)
			if d.IsDir() {
				return os.MkdirAll(dstPath, 0o755)
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(dstPath, data, 0o644)
		})
		if err != nil {
			t.Fatalf("copy %s: %v", srcPath, err)
		}
	}
}

// TestPublicDocsHaveNoInternalWording defends the public docs (README.md,
// CHANGELOG.md, docs/ and skills/) against internal wording leaking in.
func TestPublicDocsHaveNoInternalWording(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	files := publicDocFiles(t, root)

	// The skills tree must actually be walked. This guard skipped it once, so
	// the committed SKILL.md went unchecked while every other file passed; a
	// root dropped from publicDocRoots may leave the loop below green.
	walkedSkill := false
	for _, f := range files {
		if strings.HasSuffix(filepath.ToSlash(f), "skills/llm-semantic/SKILL.md") {
			walkedSkill = true
		}
	}
	if !walkedSkill {
		t.Fatal("the wording guard does not walk skills/llm-semantic/SKILL.md, so forbidden wording in an instruction file reaches every agent that loads the skill unchecked")
	}

	for _, v := range wordingViolations(t, files) {
		t.Error(v)
	}
}

// TestPublicWordingGuardWalksSkills defends the guard above: a forbidden word
// planted in a copy of the committed SKILL.md must fail. It copies the guarded
// roots through publicDocRoots, so dropping skills/ from that list leaves the
// planted word unscanned and turns this test red, which is what proves the
// guard covers skills/ rather than merely claiming to.
func TestPublicWordingGuardWalksSkills(t *testing.T) {
	root := t.TempDir()
	copyPublicDocTree(t, filepath.Join("..", "..", ".."), root)

	skill := filepath.Join(root, "skills", "llm-semantic", "SKILL.md")
	data, err := os.ReadFile(skill)
	if err != nil {
		t.Fatalf("read copied SKILL.md: %v", err)
	}
	planted := string(data) + "\nTracked as EST-9552.\nA board decision chose this.\n"
	if err := os.WriteFile(skill, []byte(planted), 0o644); err != nil {
		t.Fatalf("plant wording in copied SKILL.md: %v", err)
	}

	violations := wordingViolations(t, publicDocFiles(t, root))
	if len(violations) != 2 {
		t.Fatalf("planted wording in SKILL.md gave %d violations, want 2 (EST-9552 and board decision): %v", len(violations), violations)
	}
	for _, want := range []string{"EST-9552", "board decision"} {
		found := false
		for _, v := range violations {
			if strings.Contains(v, "skills/llm-semantic/SKILL.md") && strings.Contains(v, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("planted %q in SKILL.md was not reported against skills/llm-semantic/SKILL.md: %v", want, violations)
		}
	}
}

// TestReadmeStatesProfileIndexDefault defends the rule resolveProfileIndexDir
// applies: with no config and no explicit --index-dir, a non-code profile uses
// .index/<profile> and `code` keeps .index. The README said the opposite once
// ("give each profile its own --index-dir"), which left users sharing .index,
// so a docs search answered from code chunks. The wording carries the rule, so
// it is checked with the behaviour tests that pin the resolver.
func TestReadmeStatesProfileIndexDefault(t *testing.T) {
	readme := readPublicDoc(t, "README.md")

	para := paragraphStartingWith(readme, "`--profile` names the index being addressed:")
	if para == "" {
		t.Fatal("README.md lost the paragraph naming what --profile addresses")
	}
	for _, want := range []string{".index/<profile>", "--index-dir", "config"} {
		if !strings.Contains(para, want) {
			t.Errorf("README.md --profile paragraph no longer names %q, so the default index per profile is unstated", want)
		}
	}
	if strings.Contains(para, "give each profile its own `--index-dir`") {
		t.Error("README.md tells users to pass --index-dir for every profile; a non-code profile already defaults to .index/<profile>")
	}
	if !strings.Contains(readme, "`collection delete --profile <name>` deletes from that profile's own index") {
		t.Error("README.md no longer says collection delete acts on the profile's own index, which is the false-empty delete this documents")
	}
}

// TestReadmeStatesSetupOpencodeLimits defends two limits of `setup`'s OpenCode
// target that a user only learned by losing work or by a plugin that silently
// did nothing: setup writes .opencode/plugins/llm-semantic.ts as fixed content
// (planFixedFile), so a re-run replaces local edits instead of merging them,
// and the generated plugin runs its command through `sh -c`, so a machine
// without `sh` on PATH gets no session status. The README called setup "safe to
// re-run" without saying the first, and said nothing about the second.
func TestReadmeStatesSetupOpencodeLimits(t *testing.T) {
	readme := readPublicDoc(t, "README.md")

	para := paragraphStartingWith(readme, "- `llm-semantic setup`")
	if para == "" {
		t.Fatal("README.md lost the setup paragraph")
	}
	// The sentence must name the file and say edits to it do not survive.
	if !strings.Contains(para, "`.opencode/plugins/llm-semantic.ts`") {
		t.Error("README.md setup paragraph no longer names .opencode/plugins/llm-semantic.ts")
	}
	if !strings.Contains(para, "local edits") || !strings.Contains(para, "overwritten") {
		t.Error("README.md setup paragraph does not say setup overwrites local edits to .opencode/plugins/llm-semantic.ts, so a user who customises the plugin loses the edit on the next run")
	}
	// The OpenCode plugin shell is the reason `sh` is required; keep both.
	if !strings.Contains(para, "`sh`") || !strings.Contains(para, "`PATH`") {
		t.Error("README.md setup paragraph does not say the OpenCode plugin needs sh on PATH, so a missing sh reads as a plugin that silently does nothing")
	}

	// The sentences stay true: the generated plugin runs through sh -c and its
	// own header says a rerun overwrites edits.
	plugin := string(opencodePluginContent("llm-semantic index-status"))
	if !strings.Contains(plugin, "sh -c") {
		t.Error("opencodePluginContent no longer runs the command through sh -c, so the README's sh requirement is stale")
	}
	if !strings.Contains(plugin, "edits here are overwritten") {
		t.Error("opencodePluginContent no longer says a rerun overwrites edits, so the README's overwrite sentence is stale")
	}
}
