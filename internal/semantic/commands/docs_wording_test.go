package commands

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestPublicDocsHaveNoInternalWording defends the public docs (README.md,
// CHANGELOG.md, docs/) against internal ticket IDs, epic labels, board-decision
// wording, pre-05.1 labels and private hostnames or addresses leaking in.
func TestPublicDocsHaveNoInternalWording(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	forbidden := regexp.MustCompile(`EST-\d+|board decision|\(epic \d|pre-05\.1|\.lan\b|192\.168\.|estr\.in`)

	files := []string{filepath.Join(root, "README.md"), filepath.Join(root, "CHANGELOG.md")}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, p)
		}
		return err
	})
	if err != nil {
		t.Fatalf("walk docs: %v", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if m := forbidden.FindString(line); m != "" {
				t.Errorf("%s:%d contains %q", f, i+1, m)
			}
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
