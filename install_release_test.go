package llmsemantic_test

// These tests run install-release.sh against a fake release served from a
// local directory over loopback. They defend the download install path: a
// user who runs the installer must end up with both binaries or with nothing
// changed, never with a half-written or unverified binary that looks installed.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	fakeCLI = "#!/bin/sh\necho fake-llm-semantic\n"
	fakeMCP = "#!/bin/sh\necho fake-llm-semantic-mcp\n"
)

// archiveName is the asset the installer must ask for on this machine. It
// mirrors the name release.yml publishes; TestReleaseWorkflowPublishesInstallerArchives
// pins that side of the contract.
func archiveName() string {
	return fmt.Sprintf("llm-semantic-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
}

// makeArchive builds a tar.gz holding the given files at its root, the shape
// release.yml packs.
func makeArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fakeRelease writes a release directory: the archive plus a SHA256SUMS.txt
// in sha256sum's format. sumsFor is the content the checksum line is computed
// from, so a test can make the published sum disagree with the archive.
func fakeRelease(t *testing.T, archive, sumsFor []byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, archiveName()), archive, 0o644); err != nil {
		t.Fatal(err)
	}
	sums := fmt.Sprintf("%s  llm-semantic-other-arch.tar.gz\n%s  %s\n", strings.Repeat("0", 64), sha256Hex(sumsFor), archiveName())
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS.txt"), []byte(sums), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// restrictedPath builds a PATH holding only the tools the installer is
// allowed to use. go, git and sudo are not on it, so an installer that
// reached for any of them fails here. downloader is "curl" or "wget"; the
// other one is left out so each download path is exercised on its own.
func restrictedPath(t *testing.T, downloader string) string {
	t.Helper()
	dir := t.TempDir()
	tools := []string{"uname", "mktemp", "tar", "gzip", "sha256sum", "shasum", "awk", "mv", "cp", "chmod", "mkdir", "rm", "rmdir", "cat", "sh", downloader}
	for _, tool := range tools {
		src, err := exec.LookPath(tool)
		if err != nil {
			if tool == downloader {
				t.Skipf("%s is not installed", tool)
			}
			continue // sha256sum and shasum are alternatives; one is enough
		}
		if err := os.Symlink(src, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type installRun struct {
	home, tmp, out string
	err            error
}

func (r installRun) installDir() string { return filepath.Join(r.home, ".local", "bin") }

// runInstaller copies install-release.sh into a fresh directory, outside any
// checkout, and runs it with a fresh HOME and TMPDIR. An extraEnv entry of the
// form PATH+=dir appends dir to the restricted PATH.
func runInstaller(t *testing.T, baseURL, downloader string, extraEnv ...string) installRun {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install-release.sh targets Linux and macOS")
	}
	script, err := os.ReadFile("install-release.sh")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "install-release.sh"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	r := installRun{home: filepath.Join(work, "home"), tmp: filepath.Join(work, "tmp")}
	for _, d := range []string{r.home, r.tmp} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	// A hard bound, so an installer that ignores its own timeout fails here
	// instead of hanging the suite.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sh, filepath.Join(work, "install-release.sh"))
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = work
	path := restrictedPath(t, downloader)
	env := []string{"HOME=" + r.home, "TMPDIR=" + r.tmp, "LLM_SEMANTIC_BASE_URL=" + baseURL}
	for _, e := range extraEnv {
		if extra, ok := strings.CutPrefix(e, "PATH+="); ok {
			path += string(os.PathListSeparator) + extra
			continue
		}
		env = append(env, e)
	}
	cmd.Env = append(env, "PATH="+path)
	out, err := cmd.CombinedOutput()
	r.out, r.err = string(out), err
	return r
}

// assertNothingLeft checks a failed run touched nothing: no install
// directory created and no temp file or directory left behind.
func assertNothingLeft(t *testing.T, r installRun) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(r.home, ".local")); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(r.installDir())
		t.Errorf("failed install left %s behind (entries %v)", filepath.Join(r.home, ".local"), entries)
	}
	if entries, _ := os.ReadDir(r.tmp); len(entries) != 0 {
		t.Errorf("failed install left temp files: %v", entries)
	}
}

func assertInstalled(t *testing.T, r installRun) {
	t.Helper()
	if r.err != nil {
		t.Fatalf("installer failed: %v\n%s", r.err, r.out)
	}
	for name, want := range map[string]string{"llm-semantic": fakeCLI, "llm-semantic-mcp": fakeMCP} {
		p := filepath.Join(r.installDir(), name)
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s not installed: %v\n%s", name, err, r.out)
		}
		if string(got) != want {
			t.Errorf("%s content = %q, want %q", name, got, want)
		}
		info, _ := os.Stat(p)
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable: %v", name, info.Mode())
		}
	}
	entries, _ := os.ReadDir(r.installDir())
	if len(entries) != 2 {
		t.Errorf("install dir holds %d entries, want exactly the two binaries (no staged temp files): %v", len(entries), entries)
	}
	if entries, _ := os.ReadDir(r.tmp); len(entries) != 0 {
		t.Errorf("successful install left temp files: %v", entries)
	}
}

func goodArchive(t *testing.T) []byte {
	return makeArchive(t, map[string]string{"llm-semantic": fakeCLI, "llm-semantic-mcp": fakeMCP})
}

// Defends the happy path: both binaries land in ~/.local/bin from a
// download alone, with no sudo, git or go on PATH and no checkout around.
func TestInstallRelease_InstallsBothBinariesWithoutSudo(t *testing.T) {
	for _, dl := range []string{"curl", "wget"} {
		t.Run(dl, func(t *testing.T) {
			archive := goodArchive(t)
			srv := httptest.NewServer(http.FileServer(http.Dir(fakeRelease(t, archive, archive))))
			defer srv.Close()

			r := runInstaller(t, srv.URL, dl)
			assertInstalled(t, r)
			// The MCP server finds the CLI through PATH, so a user whose PATH
			// lacks ~/.local/bin gets a server that cannot run the CLI. The
			// installer must say so.
			if !strings.Contains(r.out, "not on your PATH") || !strings.Contains(r.out, "llm-semantic-mcp") {
				t.Errorf("installer did not warn that %s must be on PATH:\n%s", r.installDir(), r.out)
			}
		})
	}
}

// Defends the PATH warning from firing when there is nothing to warn about,
// which would train users to ignore it.
func TestInstallRelease_NoPathWarningWhenOnPath(t *testing.T) {
	archive := goodArchive(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(fakeRelease(t, archive, archive))))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "bin")
	r := runInstaller(t, srv.URL, "curl", "LLM_SEMANTIC_INSTALL_DIR="+dir, "PATH+="+dir)
	if r.err != nil {
		t.Fatalf("installer failed: %v\n%s", r.err, r.out)
	}
	for _, name := range []string{"llm-semantic", "llm-semantic-mcp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("LLM_SEMANTIC_INSTALL_DIR not honoured: %v", err)
		}
	}
	if strings.Contains(r.out, "not on your PATH") {
		t.Errorf("installer warned about PATH although %s is on it:\n%s", dir, r.out)
	}
}

// Defends against installing a binary that is not the one published: a
// SHA256SUMS.txt that disagrees with the archive must stop the install
// before anything is written.
func TestInstallRelease_RefusesChecksumMismatch(t *testing.T) {
	archive := goodArchive(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(fakeRelease(t, archive, []byte("something else")))))
	defer srv.Close()

	r := runInstaller(t, srv.URL, "curl")
	if r.err == nil {
		t.Fatalf("installer exited 0 on a checksum mismatch:\n%s", r.out)
	}
	if !strings.Contains(strings.ToLower(r.out), "checksum mismatch") {
		t.Errorf("output does not name the checksum mismatch:\n%s", r.out)
	}
	assertNothingLeft(t, r)
}

// Defends against trusting an archive the release does not list: no line
// in SHA256SUMS.txt means nothing to verify against, which is a refusal,
// not a pass.
func TestInstallRelease_RefusesArchiveMissingFromSums(t *testing.T) {
	archive := goodArchive(t)
	dir := fakeRelease(t, archive, archive)
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS.txt"), []byte(strings.Repeat("0", 64)+"  llm-semantic-other-arch.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer srv.Close()

	r := runInstaller(t, srv.URL, "curl")
	if r.err == nil {
		t.Fatalf("installer exited 0 with no checksum for the archive:\n%s", r.out)
	}
	assertNothingLeft(t, r)
}

// Defends against a download cut off mid-transfer. The server promises the
// whole archive and closes the connection halfway, the way a dropped network
// does.
func TestInstallRelease_RefusesTruncatedDownload(t *testing.T) {
	archive := goodArchive(t)
	dir := fakeRelease(t, archive, archive)
	files := http.FileServer(http.Dir(dir))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.HasSuffix(req.URL.Path, ".tar.gz") {
			files.ServeHTTP(w, req)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(archive[:len(archive)/2])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // drop the connection without the rest
	}))
	defer srv.Close()

	for _, dl := range []string{"curl", "wget"} {
		t.Run(dl, func(t *testing.T) {
			r := runInstaller(t, srv.URL, dl)
			if r.err == nil {
				t.Fatalf("installer exited 0 on a truncated download:\n%s", r.out)
			}
			assertNothingLeft(t, r)
		})
	}
}

// Defends the same as the truncated download, for the case a downloader
// does not notice: the server sends a short body with a matching length, so
// only the checksum catches it.
func TestInstallRelease_RefusesShortArchiveServedCleanly(t *testing.T) {
	archive := goodArchive(t)
	dir := fakeRelease(t, archive, archive)
	if err := os.WriteFile(filepath.Join(dir, archiveName()), archive[:len(archive)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer srv.Close()

	r := runInstaller(t, srv.URL, "curl")
	if r.err == nil {
		t.Fatalf("installer exited 0 on a short archive:\n%s", r.out)
	}
	assertNothingLeft(t, r)
}

// Defends the pair: an archive holding only one binary must not install
// it, since the MCP server alone cannot do anything and a CLI alone leaves
// an older server pointing at a newer CLI.
func TestInstallRelease_RefusesArchiveMissingABinary(t *testing.T) {
	archive := makeArchive(t, map[string]string{"llm-semantic": fakeCLI})
	srv := httptest.NewServer(http.FileServer(http.Dir(fakeRelease(t, archive, archive))))
	defer srv.Close()

	r := runInstaller(t, srv.URL, "curl")
	if r.err == nil {
		t.Fatalf("installer exited 0 with llm-semantic-mcp missing from the archive:\n%s", r.out)
	}
	assertNothingLeft(t, r)
}

// Defends an existing install from a failed upgrade: the old pair must
// still be there, byte for byte, after a refused download.
func TestInstallRelease_FailureKeepsExistingInstall(t *testing.T) {
	archive := goodArchive(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(fakeRelease(t, archive, []byte("tampered")))))
	defer srv.Close()

	// A pre-existing install in a directory the installer is pointed at.
	existing := t.TempDir()
	for _, name := range []string{"llm-semantic", "llm-semantic-mcp"} {
		if err := os.WriteFile(filepath.Join(existing, name), []byte("old "+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r := runInstaller(t, srv.URL, "curl", "LLM_SEMANTIC_INSTALL_DIR="+existing)
	if r.err == nil {
		t.Fatalf("installer exited 0 on a checksum mismatch:\n%s", r.out)
	}
	entries, _ := os.ReadDir(existing)
	if len(entries) != 2 {
		t.Errorf("install dir holds %v after a refused upgrade, want only the two old binaries", entries)
	}
	for _, name := range []string{"llm-semantic", "llm-semantic-mcp"} {
		got, _ := os.ReadFile(filepath.Join(existing, name))
		if string(got) != "old "+name {
			t.Errorf("%s changed to %q after a refused upgrade", name, got)
		}
	}
}

// Defends the download timeout: a server that accepts the connection and
// never answers must not hang the installer.
func TestInstallRelease_TimesOutOnStalledServer(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release) // unblock the handler before srv.Close waits on it

	start := time.Now()
	r := runInstaller(t, srv.URL, "curl", "LLM_SEMANTIC_INSTALL_TIMEOUT=2")
	if r.err == nil {
		t.Fatalf("installer exited 0 against a stalled server:\n%s", r.out)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("installer took %v against a stalled server; the timeout is not applied", elapsed)
	}
	assertNothingLeft(t, r)
}

// Defends the contract between the two halves of the install path: the
// installer asks for llm-semantic-<os>-<arch>.tar.gz and SHA256SUMS.txt, so
// release.yml must publish exactly those, for every Linux and macOS target.
func TestReleaseWorkflowPublishesInstallerArchives(t *testing.T) {
	wf, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("install-release.sh")
	if err != nil {
		t.Fatal(err)
	}
	const asset = `llm-semantic-${os}-${arch}.tar.gz`
	for name, body := range map[string]string{"release.yml": string(wf), "install-release.sh": string(script)} {
		if !strings.Contains(body, asset) {
			t.Errorf("%s does not name the archive as %s", name, asset)
		}
		if !strings.Contains(body, "SHA256SUMS.txt") {
			t.Errorf("%s does not name SHA256SUMS.txt", name)
		}
	}
	for _, platform := range []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"} {
		if !strings.Contains(string(wf), platform) {
			t.Errorf("release.yml does not build %s", platform)
		}
	}
}

// stampedInstaller mimics the sed in release.yml's packaging step.
func stampedInstaller(t *testing.T, tag string) string {
	t.Helper()
	script, err := os.ReadFile("install-release.sh")
	if err != nil {
		t.Fatal(err)
	}
	const placeholder = "\nPINNED_VERSION=\"\"\n"
	if !strings.Contains(string(script), placeholder) {
		t.Fatal("install-release.sh has no PINNED_VERSION placeholder line for release.yml to stamp")
	}
	return strings.Replace(string(script), placeholder, "\nPINNED_VERSION=\""+tag+"\"\n", 1)
}

// Defends against a tag's install-release.sh installing latest: a stamped
// script must resolve its own tag's download URL, an explicit --version must
// still win, and an unstamped script must keep resolving latest. A dead
// loopback proxy stops the download, so only the announced URL is checked.
func TestInstallRelease_StampedTagSelectsDownloadURL(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	const repo = "https://github.com/samestrin/llm-semantic-axi/releases/"
	cases := []struct {
		name, tag string
		args      []string
		want      string
	}{
		{"stamped", "v9.9.9", nil, repo + "download/v9.9.9"},
		{"explicit version overrides stamp", "v9.9.9", []string{"--version", "v1.2.3"}, repo + "download/v1.2.3"},
		{"unstamped is latest", "", nil, repo + "latest/download"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "install-release.sh")
			if err := os.WriteFile(script, []byte(stampedInstaller(t, c.tag)), 0o755); err != nil {
				t.Fatal(err)
			}
			sh, _ := exec.LookPath("sh")
			cmd := exec.Command(sh, append([]string{script}, c.args...)...)
			cmd.Env = []string{"HOME=" + dir, "TMPDIR=" + dir, "PATH=" + restrictedPath(t, "curl"), "https_proxy=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1"}
			out, _ := cmd.CombinedOutput()
			if !strings.Contains(string(out), "from "+c.want+"\n") {
				t.Errorf("want download from %s, got:\n%s", c.want, out)
			}
		})
	}
}

// Defends the release side of the stamp: release.yml must rewrite the
// placeholder in the published copy and fail if the rewrite did not land.
func TestReleaseWorkflowStampsInstaller(t *testing.T) {
	wf, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`^PINNED_VERSION=\"\"\$`, `> dist/install-release.sh`, `grep -qx "PINNED_VERSION=\"$VERSION\"" dist/install-release.sh`} {
		if !strings.Contains(string(wf), want) {
			t.Errorf("release.yml does not contain %q", want)
		}
	}
}
