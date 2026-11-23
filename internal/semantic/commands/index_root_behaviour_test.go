package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/v2/pkg/output"
)

// TestIndexRefusesHomeAndFilesystemRoot (EST-8278 item 2): a bare `index` run
// from $HOME walked every dotfile and project on the machine and sent them to
// the embedding API. $HOME and / are refused, exit 2, before anything is
// created or sent, unless --allow-home says the caller meant it.
func TestIndexRefusesHomeAndFilesystemRoot(t *testing.T) {
	for _, arg := range []string{"", ".", "~", "/"} {
		t.Run("index "+arg, func(t *testing.T) {
			home := newWorkspace(t)
			t.Setenv("HOME", home)
			os.WriteFile("main.go", []byte("package main\n"), 0o644)
			fe := newFakeEmbedder(t)
			args := []string{"index", "--api-url", fe.URL}
			if arg != "" {
				args = append(args, arg)
			}

			err := executeRoot(t, args...)

			if err == nil {
				t.Fatal("index accepted the home directory or /")
			}
			if code := output.ExitCodeFor(err); code != 2 {
				t.Errorf("exit code %d, want 2: %v", code, err)
			}
			if !strings.Contains(err.Error(), "--allow-home") {
				t.Errorf("message does not name --allow-home: %v", err)
			}
			if fe.callCount() != 0 {
				t.Error("the embedder was called")
			}
			if _, statErr := os.Stat(filepath.Join(home, ".index")); !os.IsNotExist(statErr) {
				t.Error(".index was created")
			}
		})
	}
}

// TestIndexAllowHomeIndexesHome: the refusal can be overridden on purpose.
func TestIndexAllowHomeIndexesHome(t *testing.T) {
	home := newWorkspace(t)
	t.Setenv("HOME", home)
	os.WriteFile("main.go", []byte("package main\n\nfunc main() {}\n"), 0o644)
	fe := newFakeEmbedder(t)

	if err := executeRoot(t, "index", ".", "--allow-home", "--skip-calibration", "--api-url", fe.URL); err != nil {
		t.Fatalf("index --allow-home: %v", err)
	}
	if fe.callCount() == 0 {
		t.Error("nothing was indexed")
	}
}

// TestIndexPlainReportsWithheldFiles (item 7): a withheld file is reported,
// not silently dropped.
func TestIndexPlainReportsWithheldFiles(t *testing.T) {
	newWorkspace(t)
	os.WriteFile("main.go", []byte("package main\n\nfunc main() {}\n"), 0o644)
	os.WriteFile("secrets.json", []byte(`{"k":"v"}`), 0o644)
	os.WriteFile("blob.txt", []byte("a\x00b"), 0o644)
	fe := newFakeEmbedder(t)

	var err error
	out := captureStdout(t, func() {
		root := RootCmd()
		root.SetArgs([]string{"index", ".", "--plain", "--skip-calibration", "--api-url", fe.URL})
		err = root.Execute()
	})
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if !strings.Contains(out, "Withheld 2 files") || !strings.Contains(out, "1 binary") || !strings.Contains(out, "1 secret_name") {
		t.Errorf("plain output does not report the withheld files:\n%s", out)
	}
}
