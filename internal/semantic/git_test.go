package semantic

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIsGitRepo(t *testing.T) {
	// Current directory should be in a git repo
	if !IsGitRepo(".") {
		t.Skip("test must be run from within a git repo")
	}

	// A temp directory should not be a git repo
	tmpDir := t.TempDir()
	if IsGitRepo(tmpDir) {
		t.Errorf("temp directory %s should not be a git repo", tmpDir)
	}
}

func TestGitRepoRoot(t *testing.T) {
	if !IsGitRepo(".") {
		t.Skip("test must be run from within a git repo")
	}

	root, err := GitRepoRoot(".")
	if err != nil {
		t.Fatalf("GitRepoRoot() error = %v", err)
	}
	if root == "" {
		t.Error("GitRepoRoot() returned empty string")
	}
}

func TestGitChangedFiles(t *testing.T) {
	// Create a temporary git repo
	tmpDir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	// Initialize repo with initial commit
	run("init")
	os.WriteFile(filepath.Join(tmpDir, "initial.txt"), []byte("hello"), 0644)
	run("add", "initial.txt")
	run("commit", "-m", "initial")

	// Add a new file
	os.WriteFile(filepath.Join(tmpDir, "added.go"), []byte("package main"), 0644)
	run("add", "added.go")
	run("commit", "-m", "add file")

	changed, deleted, err := gitChangedFiles(tmpDir, "HEAD~1")
	if err != nil {
		t.Fatalf("gitChangedFiles() error = %v", err)
	}

	if len(changed) != 1 || changed[0] != "added.go" {
		t.Errorf("changed = %v, want [added.go]", changed)
	}
	if len(deleted) != 0 {
		t.Errorf("deleted = %v, want []", deleted)
	}

	// Modify a file
	os.WriteFile(filepath.Join(tmpDir, "added.go"), []byte("package main\n// updated"), 0644)
	run("add", "added.go")
	run("commit", "-m", "modify file")

	changed, deleted, err = gitChangedFiles(tmpDir, "HEAD~1")
	if err != nil {
		t.Fatalf("gitChangedFiles() error = %v", err)
	}

	if len(changed) != 1 || changed[0] != "added.go" {
		t.Errorf("changed = %v, want [added.go]", changed)
	}

	// Delete a file
	os.Remove(filepath.Join(tmpDir, "added.go"))
	run("add", "added.go")
	run("commit", "-m", "delete file")

	changed, deleted, err = gitChangedFiles(tmpDir, "HEAD~1")
	if err != nil {
		t.Fatalf("gitChangedFiles() error = %v", err)
	}

	if len(changed) != 0 {
		t.Errorf("changed = %v, want []", changed)
	}
	if len(deleted) != 1 || deleted[0] != "added.go" {
		t.Errorf("deleted = %v, want [added.go]", deleted)
	}
}

func TestGitChangedFiles_Rename(t *testing.T) {
	tmpDir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	run("init")
	os.WriteFile(filepath.Join(tmpDir, "old.go"), []byte("package main"), 0644)
	run("add", "old.go")
	run("commit", "-m", "initial")

	// Rename file
	run("mv", "old.go", "new.go")
	run("commit", "-m", "rename")

	changed, deleted, err := gitChangedFiles(tmpDir, "HEAD~1")
	if err != nil {
		t.Fatalf("gitChangedFiles() error = %v", err)
	}

	if len(changed) != 1 || changed[0] != "new.go" {
		t.Errorf("changed = %v, want [new.go]", changed)
	}
	if len(deleted) != 1 || deleted[0] != "old.go" {
		t.Errorf("deleted = %v, want [old.go]", deleted)
	}
}

// TestGitHelpers_RejectFlagLikeRef defends against a user ref reaching git as
// an option: `--since=--output=<path>` hands gitChangedFiles the ref
// `--output=<path>`, and `git diff --name-status --output=<path> HEAD` writes
// that file. Both helpers must refuse a ref starting with "-" before git runs.
func TestGitHelpers_RejectFlagLikeRef(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	run("init")
	os.WriteFile(filepath.Join(repo, "a.go"), []byte("package main"), 0644)
	run("add", "a.go")
	run("commit", "-m", "initial")

	target := filepath.Join(t.TempDir(), "written-by-git")
	ref := "--output=" + target

	if _, _, err := gitChangedFiles(repo, ref); !errors.Is(err, errFlagLikeRef) {
		t.Errorf("gitChangedFiles(%q) error = %v, want errFlagLikeRef", ref, err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("gitChangedFiles(%q) let git write %s (stat err = %v)", ref, target, err)
	}

	// git rev-parse already fails on this ref; errors.Is pins that the helper
	// refuses it itself rather than relying on git to.
	if _, err := gitResolveCommit(repo, ref); !errors.Is(err, errFlagLikeRef) {
		t.Errorf("gitResolveCommit(%q) error = %v, want errFlagLikeRef", ref, err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("gitResolveCommit(%q) let git write %s (stat err = %v)", ref, target, err)
	}
}
