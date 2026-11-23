package semantic

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// What index and index-update may read and send to the embedding API. Before
// EST-8278 they sent every file a chunker accepted: credentials files,
// gitignored files, hidden directories such as ~/.config/gh, symlinks to files
// outside the root, and a FIFO or /dev/zero that hung the run.

// MaxIndexFileSize is the largest file indexed. Source files are far smaller;
// a larger one is almost always generated, a dump or a log.
const MaxIndexFileSize = 1 << 20

// binarySniffSize is how much of a file is checked for a NUL byte, as git does
// to decide a file is binary.
const binarySniffSize = 8000

// Reasons a file that would otherwise be indexed is withheld. They are the
// keys of IndexResult.FilesWithheld and UpdateResult.FilesWithheld.
const (
	WithheldSecretName = "secret_name"
	WithheldOutside    = "outside_root"
	WithheldNotRegular = "not_regular_file"
	WithheldTooLarge   = "too_large"
	WithheldBinary     = "binary"
)

// secretNamePatterns match, case-insensitively, the base names of files that
// commonly hold credentials. Names are matched only for data and key formats,
// so source code named after the concept (credentials.go) is still indexed.
var secretNamePatterns = func() []string {
	p := []string{
		"*.log",
		"*.pem", "*.key", "*.p12", "*.pfx", "*.jks", "*.keystore", "*.kdbx",
		"id_rsa*", "id_dsa*", "id_ecdsa*", "id_ed25519*",
		".env", ".env.*", "*.env", ".envrc", ".netrc", "_netrc", ".npmrc", ".pypirc", ".pgpass", ".htpasswd",
		"*.tfstate", "*.tfstate.*", "*.tfvars", "*.tfvars.json",
	}
	for _, stem := range []string{"credential", "secret", "service-account", "service_account"} {
		for _, ext := range []string{"json", "yaml", "yml", "toml", "ini", "cfg", "conf", "env", "xml", "properties", "txt"} {
			p = append(p, stem+"*."+ext)
		}
	}
	return p
}()

// isSecretName reports whether a file's base name marks it as likely to hold
// a credential.
func isSecretName(name string) bool {
	lower := strings.ToLower(name)
	for _, p := range secretNamePatterns {
		if ok, _ := filepath.Match(p, lower); ok {
			return true
		}
	}
	return false
}

// ignoreRule is one line of a .gitignore or .git/info/exclude, scoped to the
// directory the file applies to.
type ignoreRule struct {
	base     string // absolute directory the pattern is relative to
	pattern  string
	negate   bool
	dirOnly  bool
	anchored bool // matched against the path below base, not the base name
}

func (r ignoreRule) match(abs string, isDir bool) bool {
	if r.dirOnly && !isDir {
		return false
	}
	rel, err := filepath.Rel(r.base, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	rel = filepath.ToSlash(rel)
	if r.anchored {
		ok, _ := doublestar.Match(r.pattern, rel)
		return ok
	}
	ok, _ := doublestar.Match(r.pattern, path.Base(rel))
	return ok
}

// parseIgnoreFile reads gitignore rules from file, scoped to base. A missing
// or unreadable file has no rules.
func parseIgnoreFile(file, base string) []ignoreRule {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	var rules []ignoreRule
	sc := bufio.NewScanner(io.LimitReader(f, MaxIndexFileSize))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := ignoreRule{base: base}
		if strings.HasPrefix(line, "!") {
			r.negate = true
			line = line[1:]
		} else if strings.HasPrefix(line, `\`) {
			line = line[1:] // \# and \! escape a leading # or !
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly = true
			line = strings.TrimRight(line, "/")
		}
		if strings.Contains(line, "/") {
			r.anchored = true
			line = strings.TrimPrefix(line, "/")
		}
		if line == "" {
			continue
		}
		r.pattern = line
		rules = append(rules, r)
	}
	return rules
}

// pathPolicy decides which paths under an index root may be read: it honours
// .gitignore files (from the repository's top down to each directory) and
// .git/info/exclude, skips hidden directories below the root, withholds
// likely-secret names, and reads only regular files that resolve inside the
// root, are at most MaxIndexFileSize, and are not binary.
type pathPolicy struct {
	root     string // absolute, as walked
	realRoot string // root with symlinks resolved
	top      string // the repository's top directory, or root outside one
	exclude  []ignoreRule
	rules    map[string][]ignoreRule // per directory, loaded on first use
}

func newPathPolicy(rootPath string) (*pathPolicy, error) {
	root, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	p := &pathPolicy{root: root, realRoot: realRoot, top: root, rules: map[string][]ignoreRule{}}
	for dir := root; ; dir = filepath.Dir(dir) {
		if gitDir, ok := gitDirOf(dir); ok {
			p.top = dir
			p.exclude = parseIgnoreFile(filepath.Join(gitDir, "info", "exclude"), dir)
			break
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return p, nil
}

// gitDirOf returns the git directory of a repository whose top is dir: dir/.git
// itself, or, for a worktree or submodule, the common directory its .git file
// points to (where info/exclude lives).
func gitDirOf(dir string) (string, bool) {
	dotGit := filepath.Join(dir, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return "", false
	}
	if info.IsDir() {
		return dotGit, true
	}
	raw, err := os.ReadFile(dotGit)
	if err != nil {
		return "", false
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir:")
	if !ok {
		return "", false
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	if common, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		c := strings.TrimSpace(string(common))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitDir, c)
		}
		return c, true
	}
	return gitDir, true
}

// rulesIn returns the rules dir adds: its .gitignore, preceded, when dir is
// the top of a repository nested below p.top, by that repository's
// info/exclude. Indexing a parent of several repositories (--allow-home) read
// only the outermost repository's exclude, and sent the others' files.
func (p *pathPolicy) rulesIn(dir string) []ignoreRule {
	rules, ok := p.rules[dir]
	if !ok {
		if gitDir, isRepo := gitDirOf(dir); isRepo && dir != p.top {
			rules = parseIgnoreFile(filepath.Join(gitDir, "info", "exclude"), dir)
		}
		rules = append(rules, parseIgnoreFile(filepath.Join(dir, ".gitignore"), dir)...)
		p.rules[dir] = rules
	}
	return rules
}

// ignored reports whether git would ignore abs: the last matching rule wins,
// and a deeper .gitignore comes after a shallower one.
func (p *pathPolicy) ignored(abs string, isDir bool) bool {
	rel, err := filepath.Rel(p.top, filepath.Dir(abs))
	if err != nil || strings.HasPrefix(rel, "..") {
		return false
	}
	rules := append([]ignoreRule(nil), p.exclude...)
	dir := p.top
	rules = append(rules, p.rulesIn(dir)...)
	if rel != "." {
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			dir = filepath.Join(dir, part)
			rules = append(rules, p.rulesIn(dir)...)
		}
	}
	ignored := false
	for _, r := range rules {
		if r.match(abs, isDir) {
			ignored = !r.negate
		}
	}
	return ignored
}

// skipDir reports whether the walk should not descend into dir: a hidden
// directory below the root, or one git ignores.
func (p *pathPolicy) skipDir(dir string) bool {
	if dir == p.root {
		return false
	}
	return strings.HasPrefix(filepath.Base(dir), ".") || p.ignored(dir, true)
}

// skipPath reports whether a file is out of scope as a path: ignored by git
// or under a hidden directory below the root. index-update's git path gets
// changed files from a diff, not a walk, so it checks the directories here.
func (p *pathPolicy) skipPath(abs string) bool {
	rel, err := filepath.Rel(p.root, filepath.Dir(abs))
	if err != nil || strings.HasPrefix(rel, "..") {
		return true
	}
	if rel != "." {
		dir := p.root
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			dir = filepath.Join(dir, part)
			if p.skipDir(dir) {
				return true
			}
		}
	}
	return p.ignored(abs, false)
}

// withhold returns why a file in scope must not be read, or "". It is called
// after the include, exclude and chunker checks, so it counts only files that
// would otherwise have been indexed.
func (p *pathPolicy) withhold(abs string) string {
	if isSecretName(filepath.Base(abs)) {
		return WithheldSecretName
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return WithheldNotRegular
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return WithheldNotRegular // dangling or a loop
		}
		if !pathWithin(p.realRoot, target) {
			return WithheldOutside
		}
		if info, err = os.Stat(target); err != nil {
			return WithheldNotRegular
		}
	}
	if !info.Mode().IsRegular() {
		return WithheldNotRegular
	}
	if info.Size() > MaxIndexFileSize {
		return WithheldTooLarge
	}
	if isBinaryFile(abs) {
		return WithheldBinary
	}
	return ""
}

// pathWithin reports whether path is root or below it.
func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// isBinaryFile reports whether the start of a regular file holds a NUL byte.
func isBinaryFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false // the read reports it
	}
	defer f.Close()
	buf := make([]byte, binarySniffSize)
	n, _ := io.ReadFull(f, buf)
	return bytes.IndexByte(buf[:n], 0) >= 0
}

// readIndexFile reads a file to index. It refuses anything that is not a
// regular file or is larger than MaxIndexFileSize, so a file swapped for a
// FIFO or a device after the walk cannot block or exhaust memory.
func readIndexFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > MaxIndexFileSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, MaxIndexFileSize)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxIndexFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxIndexFileSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, MaxIndexFileSize)
	}
	return b, nil
}
