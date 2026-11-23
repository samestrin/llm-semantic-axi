package semantic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EST-8336 Low 3: a .index symlink pointing outside was followed and the
// SQLite database written there (an empty outside file became a database).
// A symlinked index directory, database or sidecar file is refused, and the
// target is left as it was.
func TestIndexStoresRefuseASymlinkedPath(t *testing.T) {
	openers := map[string]func(string) error{
		"sqlite": func(p string) error {
			s, err := NewSQLiteStorage(p, 4)
			if err == nil {
				s.Close()
			}
			return err
		},
		"lexical": func(p string) error {
			l, err := NewLexicalIndex(p, 4)
			if err == nil {
				l.Close()
			}
			return err
		},
	}
	for name, open := range openers {
		t.Run(name+"/database", func(t *testing.T) {
			dir := t.TempDir()
			outside := filepath.Join(t.TempDir(), "victim")
			if err := os.WriteFile(outside, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			db := filepath.Join(dir, ".index", "semantic.db")
			os.MkdirAll(filepath.Dir(db), 0o755)
			if err := os.Symlink(outside, db); err != nil {
				t.Fatal(err)
			}
			assertRefused(t, open(db), db)
			if info, _ := os.Stat(outside); info.Size() != 0 {
				t.Errorf("the symlink's target was written: %d bytes", info.Size())
			}
		})
		t.Run(name+"/directory", func(t *testing.T) {
			dir := t.TempDir()
			outside := t.TempDir()
			if err := os.Symlink(outside, filepath.Join(dir, ".index")); err != nil {
				t.Fatal(err)
			}
			db := filepath.Join(dir, ".index", "semantic.db")
			assertRefused(t, open(db), filepath.Join(dir, ".index"))
			if entries, _ := os.ReadDir(outside); len(entries) != 0 {
				t.Errorf("files were written through the symlinked directory: %v", entries)
			}
		})
		t.Run(name+"/wal", func(t *testing.T) {
			dir := t.TempDir()
			outside := filepath.Join(t.TempDir(), "victim")
			os.WriteFile(outside, nil, 0o644)
			db := filepath.Join(dir, "semantic.db")
			if err := os.Symlink(outside, db+"-wal"); err != nil {
				t.Fatal(err)
			}
			assertRefused(t, open(db), db+"-wal")
		})
	}
}

func assertRefused(t *testing.T, err error, path string) {
	t.Helper()
	if err == nil {
		t.Fatal("opened an index through a symlink")
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error %q should name %s as a symlink", err, path)
	}
}
