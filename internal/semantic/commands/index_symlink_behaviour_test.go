package commands

import (
	"os"
	"strings"
	"testing"
)

// EST-8336 Low 3: `index .` followed a .index symlink pointing outside the
// project, and an empty file there became the database. It is refused before
// anything is written or sent; index-update and search refuse it too.
func TestIndexRefusesASymlinkedIndexDir(t *testing.T) {
	for _, args := range [][]string{{"index", "."}, {"index-update", "--no-git"}, {"search", "x"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			newWorkspace(t)
			os.WriteFile("main.go", []byte("package main\n\nfunc main() {}\n"), 0o644)
			outside := t.TempDir()
			os.WriteFile(outside+"/semantic.db", nil, 0o644)
			if err := os.Symlink(outside, ".index"); err != nil {
				t.Fatal(err)
			}
			fe := newFakeEmbedder(t)
			useEmbedder(t, fe)

			err := executeRoot(t, append(args, "--api-url", fe.URL)...)

			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("err = %v, want a symlink refusal", err)
			}
			if entries, _ := os.ReadDir(outside); len(entries) != 1 {
				t.Errorf("files were created through the symlink: %v", entries)
			}
			if info, _ := os.Stat(outside + "/semantic.db"); info.Size() != 0 {
				t.Errorf("the outside file became a database: %d bytes", info.Size())
			}
			if n := fe.callCount(); n != 0 {
				t.Errorf("embedder received %d calls", n)
			}
		})
	}
}
