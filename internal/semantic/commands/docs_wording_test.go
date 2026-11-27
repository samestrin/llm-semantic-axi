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
