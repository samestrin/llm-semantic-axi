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
