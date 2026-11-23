package llmsemantic_test

// These tests run install.sh, the from-source installer, with a fake `go` and
// a PATH holding only the tools it needs. They defend the review round 2 item
// 3 findings: on stock macOS, which has no `timeout`, `sudo ./install.sh`
// installed both binaries and then exited 127 in its verify step; and a run
// from another directory stamped that directory's commit into the binaries.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGo records the -ldflags of each build in the binary it "builds", which
// exits 0 like the real ones do for --version and for an empty MCP stdin.
const fakeGo = `#!/bin/sh
out=""; ld=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift ;;
    -ldflags) ld="$2"; shift ;;
  esac
  shift
done
printf '#!/bin/sh\n# ldflags: %s\nexit 0\n' "$ld" > "$out"
chmod +x "$out"
`

// installEnv copies install.sh into a fresh git repository with one commit,
// and builds a bin directory holding only the tools the script needs, plus
// the given extra scripts. It returns the repo, the bin dir and the commit.
func installEnv(t *testing.T, extra map[string]string) (repo, bin, commit string) {
	t.Helper()
	repo = t.TempDir()
	src, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "install.sh"), src, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("add", "install.sh")
	git("commit", "-q", "-m", "init")
	commit = git("rev-parse", "--short", "HEAD")

	bin = t.TempDir()
	for _, tool := range []string{"git", "dirname", "mkdir", "rm", "cp", "chmod"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not on PATH: %v", tool, err)
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	scripts := map[string]string{"go": fakeGo}
	for name, body := range extra {
		scripts[name] = body
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return repo, bin, commit
}

// runInstall runs install.sh from an unrelated directory into a temp INSTALL_DIR.
func runInstall(t *testing.T, repo, bin string) (string, string, int) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	dest := t.TempDir()
	cmd := exec.Command(bash, filepath.Join(repo, "install.sh"))
	cmd.Dir = t.TempDir() // not a git repository, and not the script's
	cmd.Env = []string{"PATH=" + bin, "HOME=" + t.TempDir(), "INSTALL_DIR=" + dest}
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), dest, code
}

// TestInstallShVerifiesWithoutTimeout: with neither `timeout` nor `gtimeout`
// on PATH, as on stock macOS, the install must verify both binaries and exit
// 0, and stamp the commit of the script's own repository.
func TestInstallShVerifiesWithoutTimeout(t *testing.T) {
	repo, bin, commit := installEnv(t, nil)
	out, dest, code := runInstall(t, repo, bin)
	if code != 0 || !strings.Contains(out, "All binaries installed successfully") {
		t.Fatalf("install.sh exit %d without timeout on PATH:\n%s", code, out)
	}
	cli, err := os.ReadFile(filepath.Join(dest, "llm-semantic"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cli), "main.commit="+commit) {
		t.Errorf("run from another directory stamped the wrong commit; want %s:\n%s", commit, cli)
	}
	if _, err := os.Stat(filepath.Join(dest, "llm-semantic-mcp")); err != nil {
		t.Errorf("llm-semantic-mcp not installed: %v", err)
	}
}

// TestInstallShUsesGtimeoutWhenThatIsAllThereIs: macOS with coreutils from
// Homebrew has `gtimeout`, not `timeout`; the verify step must use it.
func TestInstallShUsesGtimeoutWhenThatIsAllThereIs(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "gtimeout-used")
	gtimeout := "#!/bin/sh\necho x >> " + marker + "\nshift\nexec \"$@\"\n"
	repo, bin, _ := installEnv(t, map[string]string{"gtimeout": gtimeout})
	out, _, code := runInstall(t, repo, bin)
	if code != 0 {
		t.Fatalf("install.sh exit %d with only gtimeout on PATH:\n%s", code, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("verify step did not use gtimeout:\n%s", out)
	}
}

// TestInstallShCreatesAMissingInstallDir defends the review of EST-8249: an
// INSTALL_DIR that did not exist yet failed the writability check and was
// misreported as "must be run with sudo" (and, as root, died at cp).
func TestInstallShCreatesAMissingInstallDir(t *testing.T) {
	repo, bin, _ := installEnv(t, nil)
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	dest := filepath.Join(t.TempDir(), "not", "yet")
	cmd := exec.Command(bash, filepath.Join(repo, "install.sh"))
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=" + bin, "HOME=" + t.TempDir(), "INSTALL_DIR=" + dest}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh into a missing INSTALL_DIR failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dest, "llm-semantic")); err != nil {
		t.Errorf("llm-semantic not installed into %s: %v", dest, err)
	}
}
