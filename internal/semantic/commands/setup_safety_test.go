package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/v2/pkg/output"
)

// EST-8278 items 3 and 4: setup must write only under the project directory,
// and must never drop configuration it could not fully parse.

// assertEmptyDir fails when dir has any entry.
func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("%s was written to: %v", dir, entries)
	}
}

// TestSetupRefusesTrailingJSON: a settings file holding two JSON values was
// decoded up to the first and rewritten without the second, silently losing
// configuration. Anything after the object must refuse the merge.
func TestSetupRefusesTrailingJSON(t *testing.T) {
	for _, body := range []string{"{\"a\":1}\n{\"b\":2}\n", `{"a":1} trailing`, `{"a":1}]`} {
		dir := t.TempDir()
		p := filepath.Join(dir, ".claude", "settings.json")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)

		err := runSetup(&bytes.Buffer{}, dir, setupOptions{})

		if err == nil {
			t.Errorf("%q: setup accepted a settings file with trailing data", body)
		} else if !strings.Contains(err.Error(), "not a single JSON object") {
			t.Errorf("%q: error does not say what is wrong: %v", body, err)
		} else if code := output.ExitCodeFor(err); code != 1 {
			t.Errorf("%q: exit code %d, want 1", body, code)
		}
		if got, _ := os.ReadFile(p); string(got) != body {
			t.Errorf("%q: settings rewritten to %q", body, got)
		}
		if _, err := os.Stat(filepath.Join(dir, ".claude", "skills")); err == nil {
			t.Errorf("%q: setup wrote the skill after refusing the settings", body)
		}
	}
}

// TestSetupRefusesSymlinkOutOfProject: a .claude (or deeper component, or the
// file itself) that is a symlink out of the project sent setup's writes to
// wherever it pointed, against the promise that only files under the
// directory are written.
func TestSetupRefusesSymlinkOutOfProject(t *testing.T) {
	links := []struct {
		target string // setup target to run
		link   string // path in the project made a symlink
		file   bool   // the link stands for a file, not a directory
	}{
		{"claude", ".claude", false},
		{"claude", filepath.Join(".claude", "skills"), false},
		{"claude", filepath.Join(".claude", "settings.json"), true},
		{"codex", ".codex", false},
		{"opencode", filepath.Join(".opencode", "plugins"), false},
	}
	for _, l := range links {
		dir, outside := t.TempDir(), t.TempDir()
		target := outside
		if l.file {
			target = filepath.Join(outside, "settings.json")
		}
		os.MkdirAll(filepath.Dir(filepath.Join(dir, l.link)), 0o755)
		if err := os.Symlink(target, filepath.Join(dir, l.link)); err != nil {
			t.Fatal(err)
		}

		err := runSetup(&bytes.Buffer{}, dir, setupOptions{targets: []string{l.target}})

		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Errorf("%s: setup followed a symlink out of the project: %v", l.link, err)
		}
		assertEmptyDir(t, outside)
	}
}

// TestSetupAllowsSymlinkInsideProject: a symlink that stays in the project is
// an ordinary layout, not an escape, and keeps working.
func TestSetupAllowsSymlinkInsideProject(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "agent-config"), 0o755)
	if err := os.Symlink(filepath.Join(dir, "agent-config"), filepath.Join(dir, ".claude")); err != nil {
		t.Fatal(err)
	}

	if err := runSetup(&bytes.Buffer{}, dir, setupOptions{targets: []string{"claude"}}); err != nil {
		t.Fatalf("setup refused an in-project symlink: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent-config", "settings.json")); err != nil {
		t.Errorf("settings not written through the in-project symlink: %v", err)
	}
}

// TestSetupRefusesHomeDirectory: run in $HOME, setup wrote the global
// ~/.claude and ~/.codex configuration of every project. It is a usage error.
func TestSetupRefusesHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	err := runSetup(&bytes.Buffer{}, home, setupOptions{})

	if err == nil {
		t.Fatal("setup ran in the home directory")
	}
	if code := output.ExitCodeFor(err); code != 2 {
		t.Errorf("exit code %d, want 2: %v", code, err)
	}
	if !strings.Contains(err.Error(), "home directory") {
		t.Errorf("error does not name the home directory: %v", err)
	}
	assertEmptyDir(t, home)
}
