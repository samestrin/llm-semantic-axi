package commands

import (
	"os"
	"strings"
	"testing"
	"time"
)

// EST-8336 Low 4: `memory import --source /dev/zero` hung reading forever. The
// source must be a regular file no larger than the cap, refused before the
// embedder is called.
func TestMemoryImportRefusesANonRegularOrHugeSource(t *testing.T) {
	newWorkspace(t)
	big, _ := os.Create("big.yaml")
	big.Truncate(maxImportSourceSize + 1)
	big.Close()
	os.Symlink("/dev/zero", "zero.yaml")
	for _, src := range []string{"/dev/zero", "zero.yaml", "big.yaml", "."} {
		t.Run(src, func(t *testing.T) {
			fe := newFakeEmbedder(t)
			useEmbedder(t, fe)
			done := make(chan error, 1)
			go func() { done <- executeRoot(t, "memory", "import", "--source", src, "--api-url", fe.URL) }()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), src) {
					t.Errorf("err = %v, want a refusal naming %s", err, src)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("memory import hung on the source")
			}
			if n := fe.callCount(); n != 0 {
				t.Errorf("embedder received %d calls", n)
			}
		})
	}
}

// EST-8336 Low 4: `memory store -f` overwrote an existing file and wrote
// through a symlink without asking. An existing file needs --force; a symlink
// is refused even with it. Both are refused before the embedder is called.
func TestMemoryStoreFilePathRefusesOverwriteAndSymlink(t *testing.T) {
	newWorkspace(t)
	os.WriteFile("existing.md", []byte("keep me"), 0o644)
	victim := t.TempDir() + "/victim.md"
	os.WriteFile(victim, []byte("keep me"), 0o644)
	os.Symlink(victim, "link.md")
	os.Symlink(t.TempDir()+"/absent.md", "dangling.md")

	cases := []struct {
		path  string
		force bool
		check string
	}{
		{"existing.md", false, "existing.md"},
		{"link.md", false, victim},
		{"link.md", true, victim},
		{"dangling.md", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			fe := newFakeEmbedder(t)
			useEmbedder(t, fe)
			args := []string{"memory", "store", "-q", "q", "-a", "a", "-f", tc.path, "--api-url", fe.URL}
			if tc.force {
				args = append(args, "--force")
			}
			err := executeRoot(t, args...)
			if err == nil || !strings.Contains(err.Error(), tc.path) {
				t.Fatalf("err = %v, want a refusal naming %s", err, tc.path)
			}
			if tc.check != "" {
				if b, _ := os.ReadFile(tc.check); string(b) != "keep me" {
					t.Errorf("%s was overwritten: %q", tc.check, b)
				}
			}
			if n := fe.callCount(); n != 0 {
				t.Errorf("embedder received %d calls", n)
			}
		})
	}
}

// TestMemoryStoreFilePathForceOverwrites: --force replaces an existing regular
// file, and a new path is written without it.
func TestMemoryStoreFilePathForceOverwrites(t *testing.T) {
	newWorkspace(t)
	fe := newFakeEmbedder(t)
	useEmbedder(t, fe)
	os.WriteFile("existing.md", []byte("old"), 0o644)

	if err := executeRoot(t, "memory", "store", "-q", "q1", "-a", "a1", "-f", "new/m.md", "--api-url", fe.URL); err != nil {
		t.Fatalf("new path: %v", err)
	}
	if b, _ := os.ReadFile("new/m.md"); !strings.Contains(string(b), "a1") {
		t.Errorf("new/m.md = %q", b)
	}
	if err := executeRoot(t, "memory", "store", "-q", "q2", "-a", "a2", "-f", "existing.md", "--force", "--api-url", fe.URL); err != nil {
		t.Fatalf("--force: %v", err)
	}
	if b, _ := os.ReadFile("existing.md"); !strings.Contains(string(b), "a2") {
		t.Errorf("existing.md was not replaced: %q", b)
	}
}
