package semantic

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Staleness states. Only StalenessFresh means the index matches the working tree.
const (
	StalenessFresh   = "fresh"
	StalenessStale   = "stale"
	StalenessUnknown = "unknown"
)

// Reasons a staleness is unknown.
const (
	StalenessReasonNoRecord      = "no_record"      // nothing recorded: indexed before the record existed
	StalenessReasonUnsupported   = "unsupported"    // the storage backend cannot record it
	StalenessReasonReadFailed    = "read_failed"    // the record could not be read
	StalenessReasonNotGit        = "not_git"        // the directory is not in a git repository
	StalenessReasonNoCommit      = "no_commit"      // indexed outside git, so no commit to compare
	StalenessReasonNoHead        = "no_head"        // the repository has no commits
	StalenessReasonCommitUnknown = "commit_unknown" // the recorded commit is missing or not an ancestor of HEAD
	StalenessReasonGitFailed     = "git_failed"     // git could not be run, or a git command failed
)

// Staleness reports how far the index is behind the working tree.
//
// CommitsBehind and FilesChanged are nil unless State is fresh or stale: an unknown
// staleness has no counts, so it can never be read as "0 behind".
type Staleness struct {
	State         string    `json:"state"`
	Reason        string    `json:"reason,omitempty"` // set when State is unknown
	Detail        string    `json:"detail,omitempty"` // underlying error, if any
	CommitsBehind *int      `json:"commits_behind,omitempty"`
	FilesChanged  *int      `json:"files_changed,omitempty"` // uncommitted changes, untracked files included
	IndexedCommit string    `json:"indexed_commit,omitempty"`
	IndexedAt     time.Time `json:"indexed_at"`
}

// Fresh reports whether the index is known to match the working tree.
func (s Staleness) Fresh() bool {
	return s.State == StalenessFresh
}

// commitHash matches a full SHA-1 or SHA-256 object name.
var commitHash = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// CheckStaleness compares the last index recorded in storage with the git working tree
// containing dir. It uses git only (no tree walk, no embedding): commits from the recorded
// commit to HEAD, plus uncommitted changes from `git status --porcelain`, across the whole
// repository. Changes at or under any of ignore are not counted: pass the index's own
// directory, which otherwise shows up as an untracked change after every index.
// Anything that cannot be measured reports StalenessUnknown, never fresh.
func CheckStaleness(ctx context.Context, storage Storage, dir string, ignore ...string) Staleness {
	last, err := storage.GetLastIndex(ctx)
	switch {
	case errors.Is(err, ErrLastIndexUnsupported):
		return unknownStaleness(StalenessReasonUnsupported, nil)
	case err != nil:
		return unknownStaleness(StalenessReasonReadFailed, err)
	case last == nil:
		return unknownStaleness(StalenessReasonNoRecord, nil)
	}

	s := measureStaleness(ctx, last.Commit, dir, ignore)
	s.IndexedCommit = last.Commit
	s.IndexedAt = last.IndexedAt
	return s
}

func measureStaleness(ctx context.Context, recorded, dir string, ignore []string) Staleness {
	top, err := gitOutput(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		// Only git's own "not a git repository" means not_git. A missing binary, a refused
		// repository (safe.directory) or a cancelled check is a failure to measure.
		if ctx.Err() == nil && strings.Contains(gitStderr(err), "not a git repository") {
			return unknownStaleness(StalenessReasonNotGit, nil)
		}
		return gitFailed(ctx, err)
	}
	root := strings.TrimSpace(string(top))

	if recorded == "" {
		return unknownStaleness(StalenessReasonNoCommit, nil)
	}
	if _, err := gitOutput(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		return gitFailedOr(ctx, err, StalenessReasonNoHead)
	}
	// The record is data, not a trusted argument: only a full object name reaches git.
	if !commitHash.MatchString(recorded) {
		return unknownStaleness(StalenessReasonCommitUnknown, nil)
	}
	if _, err := gitOutput(ctx, dir, "rev-parse", "--verify", "--quiet", recorded+"^{commit}"); err != nil {
		return gitFailedOr(ctx, err, StalenessReasonCommitUnknown)
	}
	// A rewritten history (rebase, amend) leaves HEAD off the recorded commit's line, so
	// a count of commits since it would measure nothing.
	if _, err := gitOutput(ctx, dir, "merge-base", "--is-ancestor", recorded, "HEAD"); err != nil {
		return gitFailedOr(ctx, err, StalenessReasonCommitUnknown)
	}

	out, err := gitOutput(ctx, dir, "rev-list", "--count", recorded+"..HEAD")
	if err != nil {
		return gitFailed(ctx, err)
	}
	commits, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return unknownStaleness(StalenessReasonGitFailed, err)
	}

	out, err = gitOutput(ctx, dir, "status", "--porcelain", "-z")
	if err != nil {
		return gitFailed(ctx, err)
	}
	files := countStatusEntries(out, ignoredPrefixes(root, ignore))

	state := StalenessStale
	if commits == 0 && files == 0 {
		state = StalenessFresh
	}
	return Staleness{State: state, CommitsBehind: &commits, FilesChanged: &files}
}

func unknownStaleness(reason string, err error) Staleness {
	s := Staleness{State: StalenessUnknown, Reason: reason}
	if err != nil {
		s.Detail = err.Error()
	}
	return s
}

// gitFailed reports a git command that could not measure, with its stderr or the
// context's error as the detail.
func gitFailed(ctx context.Context, err error) Staleness {
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if msg := gitStderr(err); msg != "" {
		err = errors.New(msg)
	}
	return unknownStaleness(StalenessReasonGitFailed, err)
}

// gitFailedOr reports reason when git answered "no" (exit status 1, which the
// rev-parse --verify and --is-ancestor checks use for a negative answer), and a git
// failure otherwise.
func gitFailedOr(ctx context.Context, err error, reason string) Staleness {
	var exitErr *exec.ExitError
	if ctx.Err() == nil && errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return unknownStaleness(reason, nil)
	}
	return gitFailed(ctx, err)
}

// gitStderr returns the trimmed stderr of a failed git command, if it captured any.
func gitStderr(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return strings.TrimSpace(string(exitErr.Stderr))
	}
	return ""
}

func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	return cmd.Output()
}

// ignoredPrefixes turns paths into slash-separated paths relative to the repository root,
// as `git status --porcelain` prints them. Paths outside the repository are dropped.
func ignoredPrefixes(root string, paths []string) []string {
	root = resolvePath(root)
	var out []string
	for _, p := range paths {
		rel, err := filepath.Rel(root, resolvePath(p))
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}

// resolvePath makes p absolute with symlinks resolved, so it compares with the root git
// reports. A path that does not exist yet is resolved through its parent.
func resolvePath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if r, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(r, filepath.Base(p))
	}
	return p
}

// countStatusEntries counts the entries of `git status --porcelain -z` output, skipping
// those at or under an ignored path.
func countStatusEntries(out []byte, ignored []string) int {
	n := 0
	fields := bytes.Split(out, []byte{0})
	for i := 0; i < len(fields); i++ {
		entry := string(fields[i])
		if len(entry) < 4 {
			continue
		}
		xy, path := entry[:2], entry[3:]
		// A rename or copy is followed by a field holding its source path.
		if xy[0] == 'R' || xy[0] == 'C' {
			i++
		}
		if !isIgnoredPath(path, ignored) {
			n++
		}
	}
	return n
}

func isIgnoredPath(path string, ignored []string) bool {
	path = strings.TrimSuffix(path, "/")
	for _, ig := range ignored {
		if path == ig || strings.HasPrefix(path, ig+"/") {
			return true
		}
	}
	return false
}
