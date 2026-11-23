package semantic

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// EST-8278 items 2 and 7: `index` sent everything with a chunker to the
// embedding API. Credentials files, gitignored files, hidden directories
// ($HOME/.config/gh/hosts.yml), symlinks to files outside the root, and a
// FIFO or /dev/zero that hung the run. These tests assert what reached the
// embedder and the index, not the exit status.

// textRecorder is an embedder that remembers every text it was sent.
type textRecorder struct {
	mu    sync.Mutex
	texts []string
}

func (r *textRecorder) Embed(ctx context.Context, text string) ([]float32, error) {
	v, err := r.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return v[0], nil
}

func (r *textRecorder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	r.mu.Lock()
	r.texts = append(r.texts, texts...)
	r.mu.Unlock()
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3, 0.4}
	}
	return out, nil
}

func (r *textRecorder) Dimensions() int { return 4 }
func (r *textRecorder) Model() string   { return "recorder" }

// sent reports whether any text sent to the embedder contains s.
func (r *textRecorder) sent(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.texts {
		if strings.Contains(t, s) {
			return true
		}
	}
	return false
}

// textFactory chunks .go and the generic text types (.txt, .json, .yaml, .log ...).
func textFactory() *ChunkerFactory {
	f := goFactory()
	g := NewGenericChunker(GenericMaxChunkSize)
	for _, ext := range g.SupportedExtensions() {
		f.Register(ext, g)
	}
	return f
}

// writeTree writes files (path relative to root -> content), creating dirs.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// indexedRel lists the indexed files relative to root, sorted.
func indexedRel(t *testing.T, storage Storage, root string) []string {
	t.Helper()
	files, err := storage.ListIndexedFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for f := range files {
		rel, _ := filepath.Rel(root, f)
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

func runSafetyIndex(t *testing.T, root string) (*IndexResult, *textRecorder, Storage) {
	t.Helper()
	rec := &textRecorder{}
	storage := newFileStorage(t)
	mgr := NewIndexManager(storage, rec, textFactory())
	result, err := mgr.Index(context.Background(), root, IndexOptions{})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	return result, rec, storage
}

func assertIndexed(t *testing.T, got []string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("indexed files:\n  %v\nwant:\n  %v", got, want)
	}
}

// TestIndexWithholdsSecretFiles: credentials, secrets and logs were sent.
func TestIndexWithholdsSecretFiles(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"notes.txt":                     "ordinary notes",
		"config.yaml":                   "port: 80",
		"credentials.json":              `{"token":"LEAK-credentials"}`,
		"credentials-prod.json":         `{"token":"LEAK-credentials-prod"}`,
		"secrets.yaml":                  "token: LEAK-secrets-yaml",
		"secrets.toml":                  "token = 'LEAK-secrets-toml'",
		"service-account-key.json":      `{"private_key":"LEAK-service-account"}`,
		"server.log":                    "LEAK-log password=hunter2",
		"deploy/terraform.tfstate.json": `{"LEAK":"tfstate"}`,
	})

	result, rec, storage := runSafetyIndex(t, root)

	assertIndexed(t, indexedRel(t, storage, root), "config.yaml", "notes.txt")
	if rec.sent("LEAK") {
		t.Error("a secret file's content reached the embedding API")
	}
	if got := result.FilesWithheld["secret_name"]; got != 7 {
		t.Errorf("files_withheld.secret_name = %d, want 7 (%v)", got, result.FilesWithheld)
	}
}

// TestIndexHonoursGitignore: gitignored and .git/info/exclude'd files were sent.
func TestIndexHonoursGitignore(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".git/info/exclude":  "excluded.txt\n",
		".gitignore":         "# comment\nignored.txt\nbuild/\n*.gen.txt\n!keep.gen.txt\n/rooted.txt\ndocs/**/draft.txt\n",
		"main.txt":           "kept",
		"ignored.txt":        "LEAK-ignored",
		"excluded.txt":       "LEAK-excluded",
		"build/out.txt":      "LEAK-build",
		"x.gen.txt":          "LEAK-gen",
		"keep.gen.txt":       "kept",
		"rooted.txt":         "LEAK-rooted",
		"sub/rooted.txt":     "kept: the rule is anchored to the root",
		"docs/a/b/draft.txt": "LEAK-draft",
		"sub/.gitignore":     "local.txt\n",
		"sub/local.txt":      "LEAK-nested",
		"sub/ignored.txt":    "LEAK-unanchored",
		"other/local.txt":    "kept: sub's rule does not reach here",
	})

	_, rec, storage := runSafetyIndex(t, root)

	assertIndexed(t, indexedRel(t, storage, root), "keep.gen.txt", "main.txt", "other/local.txt", "sub/rooted.txt")
	if rec.sent("LEAK") {
		t.Error("an ignored file's content reached the embedding API")
	}
}

// TestIndexHonoursParentGitignore: indexing a subdirectory of a repository
// still applies the repository's .gitignore above it.
func TestIndexHonoursParentGitignore(t *testing.T) {
	repo := t.TempDir()
	writeTree(t, repo, map[string]string{
		".git/HEAD":           "ref: refs/heads/main\n",
		".gitignore":          "*.tmp.txt\npkg/generated/\n",
		"pkg/a.txt":           "kept",
		"pkg/b.tmp.txt":       "LEAK-tmp",
		"pkg/generated/g.txt": "LEAK-generated",
	})
	root := filepath.Join(repo, "pkg")

	_, rec, storage := runSafetyIndex(t, root)

	assertIndexed(t, indexedRel(t, storage, root), "a.txt")
	if rec.sent("LEAK") {
		t.Error("a file the parent .gitignore ignores reached the embedding API")
	}
}

// TestIndexSkipsHiddenDirectories: a bare `index` from $HOME sent
// .config/gh/hosts.yml (a GitHub token) and .docker/config.json.
func TestIndexSkipsHiddenDirectories(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".config/gh/hosts.yml": "oauth_token: LEAK-gh",
		".docker/config.json":  `{"auths":"LEAK-docker"}`,
		"project/.cache/c.txt": "LEAK-cache",
		"project/main.txt":     "kept",
		".editorconfig.txt":    "kept: hidden files are not hidden directories",
	})

	_, rec, storage := runSafetyIndex(t, root)

	assertIndexed(t, indexedRel(t, storage, root), ".editorconfig.txt", "project/main.txt")
	if rec.sent("LEAK") {
		t.Error("a hidden directory's content reached the embedding API")
	}
}

// TestIndexReadsOnlyRegularFilesInsideRoot: link.txt, a symlink to a file
// outside the folder, was read and sent; one to /dev/zero hung the run.
func TestIndexReadsOnlyRegularFilesInsideRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeTree(t, outside, map[string]string{"secret.txt": "LEAK-outside"})
	writeTree(t, root, map[string]string{"real.txt": "kept"})
	for link, target := range map[string]string{
		"link.txt":   filepath.Join(outside, "secret.txt"),
		"inside.txt": filepath.Join(root, "real.txt"),
		"zero.txt":   "/dev/zero",
		"gone.txt":   filepath.Join(root, "missing.txt"),
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}

	var result *IndexResult
	var rec *textRecorder
	var storage Storage
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, rec, storage = runSafetyIndex(t, root)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("index hung on a special file")
	}

	assertIndexed(t, indexedRel(t, storage, root), "inside.txt", "real.txt")
	if rec.sent("LEAK") {
		t.Error("a file outside the index root reached the embedding API")
	}
	if got := result.FilesWithheld["outside_root"]; got != 2 {
		t.Errorf("files_withheld.outside_root = %d, want 2 (link.txt, zero.txt): %v", got, result.FilesWithheld)
	}
	if got := result.FilesWithheld["not_regular_file"]; got != 1 {
		t.Errorf("files_withheld.not_regular_file = %d, want 1 (gone.txt): %v", got, result.FilesWithheld)
	}
}

// TestIndexWithholdsLargeAndBinaryFiles: a 9.5 MB text file and 3 MB of
// random bytes were sent whole.
func TestIndexWithholdsLargeAndBinaryFiles(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"small.txt":  "kept",
		"large.txt":  strings.Repeat("LEAK-large ", MaxIndexFileSize/11+1),
		"binary.txt": "LEAK-binary\x00\x01\x02",
		"edge.txt":   strings.Repeat("a", MaxIndexFileSize),
	})

	result, rec, storage := runSafetyIndex(t, root)

	assertIndexed(t, indexedRel(t, storage, root), "edge.txt", "small.txt")
	if rec.sent("LEAK") {
		t.Error("a large or binary file reached the embedding API")
	}
	if result.FilesWithheld["too_large"] != 1 || result.FilesWithheld["binary"] != 1 {
		t.Errorf("files_withheld = %v, want too_large 1 and binary 1", result.FilesWithheld)
	}
}

// TestIndexUpdateAppliesTheSameRules: `index-update --no-git` collects files
// the same way, and drops a file that is now withheld from the index.
func TestIndexUpdateAppliesTheSameRules(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.txt": "kept"})
	rec := &textRecorder{}
	storage := newFileStorage(t)
	mgr := NewIndexManager(storage, rec, textFactory())
	ctx := context.Background()
	if _, err := mgr.Index(ctx, root, IndexOptions{}); err != nil {
		t.Fatal(err)
	}
	writeTree(t, root, map[string]string{"secrets.json": `"LEAK"`, ".gitignore": "a.txt\n"})

	result, err := mgr.Update(ctx, root, UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	assertIndexed(t, indexedRel(t, storage, root))
	if rec.sent("LEAK") {
		t.Error("index-update sent a secret file")
	}
	if result.FilesWithheld["secret_name"] != 1 || result.FilesRemoved != 1 {
		t.Errorf("withheld %v, removed %d; want secret_name 1 and a.txt removed", result.FilesWithheld, result.FilesRemoved)
	}
}

// TestIndexUpdateGitAppliesTheSameRules: the git-diff path of index-update
// filters changed files itself, so it needs the rules too.
func TestIndexUpdateGitAppliesTheSameRules(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	commitGoFile(t, repo, "one.go", "One")
	rec := &textRecorder{}
	storage := newFileStorage(t)
	mgr := NewIndexManager(storage, rec, textFactory())
	ctx := context.Background()
	if _, err := mgr.Index(ctx, repo, IndexOptions{}); err != nil {
		t.Fatal(err)
	}
	writeTree(t, repo, map[string]string{
		"credentials.json": `"LEAK-cred"`,
		".hidden/x.txt":    "LEAK-hidden",
		"big.txt":          strings.Repeat("LEAK-big ", MaxIndexFileSize/9+1),
		"two.txt":          "kept",
		"ignored.txt":      "LEAK-ignored",
		".gitignore":       "ignored.txt\n",
	})
	// git add -A skips an ignored file; force it in, as a file tracked
	// before the rule was added would be.
	gitRun(t, repo, "add", "-f", "ignored.txt")
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "-m", "more")

	result, err := mgr.UpdateGit(ctx, repo, "HEAD~1", UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	assertIndexed(t, indexedRel(t, storage, repo), "one.go", "two.txt")
	if rec.sent("LEAK") {
		t.Error("index-update (git) sent a withheld file")
	}
	if result.FilesWithheld["secret_name"] != 1 || result.FilesWithheld["too_large"] != 1 {
		t.Errorf("files_withheld = %v, want secret_name 1 and too_large 1", result.FilesWithheld)
	}
}

// TestReadIndexFileRefusesSpecialFiles: the read itself refuses what the walk
// would have withheld, in case the file changed in between.
func TestReadIndexFileRefusesSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.txt")
	os.WriteFile(big, make([]byte, MaxIndexFileSize+1), 0o644)
	for _, p := range []string{dir, "/dev/zero", big} {
		if _, err := readIndexFile(p); err == nil {
			t.Errorf("readIndexFile(%s) read it", p)
		}
	}
	ok := filepath.Join(dir, "ok.txt")
	os.WriteFile(ok, []byte("fine"), 0o644)
	if b, err := readIndexFile(ok); err != nil || string(b) != "fine" {
		t.Errorf("readIndexFile(ok) = %q, %v", b, err)
	}
}

// TestIndexHonoursWorktreeExclude: in a git worktree or submodule .git is a
// file pointing at the git directory, whose commondir holds info/exclude.
func TestIndexHonoursWorktreeExclude(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "wt")
	writeTree(t, base, map[string]string{
		"common/info/exclude":           "excluded.txt\n",
		"common/worktrees/wt/commondir": "../..\n",
		"wt/.git":                       "gitdir: ../common/worktrees/wt\n",
		"wt/kept.txt":                   "kept",
		"wt/excluded.txt":               "LEAK-excluded",
	})

	_, rec, storage := runSafetyIndex(t, root)

	assertIndexed(t, indexedRel(t, storage, root), "kept.txt")
	if rec.sent("LEAK") {
		t.Error("a file the worktree's info/exclude names reached the embedding API")
	}
}

// TestIndexHonoursNestedRepoExclude: indexing a parent directory (as with
// --allow-home) read only the outermost repository's .git/info/exclude, so a
// file a nested repository excluded was sent (EST-8336 Low 2). Each nested
// repository's info/exclude and .gitignore apply below it, and nowhere else.
func TestIndexHonoursNestedRepoExclude(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"top.txt":                     "kept",
		"proj/.git/HEAD":              "ref: refs/heads/main\n",
		"proj/.git/info/exclude":      "private.txt\n",
		"proj/.gitignore":             "ignored.txt\n",
		"proj/main.txt":               "kept",
		"proj/private.txt":            "LEAK-excluded",
		"proj/sub/private.txt":        "LEAK-excluded-deeper",
		"proj/ignored.txt":            "LEAK-ignored",
		"other/private.txt":           "kept: proj's exclude does not reach here",
		"deep/repo/.git/HEAD":         "ref: refs/heads/main\n",
		"deep/repo/.git/info/exclude": "/notes.txt\n",
		"deep/repo/notes.txt":         "LEAK-anchored",
		"deep/repo/keep/notes.txt":    "kept: the rule is anchored to the nested repo",
	})

	_, rec, storage := runSafetyIndex(t, root)

	assertIndexed(t, indexedRel(t, storage, root),
		"deep/repo/keep/notes.txt", "other/private.txt", "proj/main.txt", "top.txt")
	if rec.sent("LEAK") {
		t.Error("a file a nested repository excludes reached the embedding API")
	}
}
