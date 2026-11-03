package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// The search commands carry how stale the index is, so a search over old code
// does not read like a search over current code. --axi puts a `stale` object in
// the payload; plain text prints a line; --json and --min keep their stdout
// schema (board decision, 2026-10-31) and print the line on stderr. A fresh index
// prints no line: only --axi, which an agent branches on, says so.

// stalenessNotApplicable is the --axi state for memory-search: memories are
// stored, not indexed from the working tree, so nothing can be behind.
const stalenessNotApplicable = "not_applicable"

// stalenessTimeout bounds the git commands of one staleness check, so a hung git
// cannot hold a search. Running out reports unknown/git_failed, never fresh.
var stalenessTimeout = 5 * time.Second

// indexStaleness compares the open index with the git working tree around the
// current directory. The index's own directory is not counted as a change.
func indexStaleness(ctx context.Context, storage semantic.Storage) semantic.Staleness {
	ctx, cancel := context.WithTimeout(ctx, stalenessTimeout)
	defer cancel()
	var ignore []string
	if indexDir != "" {
		ignore = append(ignore, indexDir)
	}
	if path := findIndexPath(); path != "" {
		ignore = append(ignore, filepath.Dir(path))
	}
	return semantic.CheckStaleness(ctx, storage, ".", ignore...)
}

// refStaleness is indexStaleness for callers and refs, whose call graph view is
// opened from a Storage.
func refStaleness(ctx context.Context, rs semantic.RefStorage) semantic.Staleness {
	storage, ok := rs.(semantic.Storage)
	if !ok {
		return semantic.Staleness{State: semantic.StalenessUnknown, Reason: semantic.StalenessReasonUnsupported}
	}
	return indexStaleness(ctx, storage)
}

// axiStaleness is the --axi `stale` object. Counts appear only when measured, and
// the index time only when one was recorded, so nothing reads as "0 behind" or as
// a time that was never recorded.
func axiStaleness(s semantic.Staleness) map[string]any {
	out := map[string]any{"state": s.State}
	if s.Reason != "" {
		out["reason"] = s.Reason
	}
	if s.Detail != "" {
		out["detail"] = s.Detail
	}
	if s.CommitsBehind != nil {
		out["commits_behind"] = *s.CommitsBehind
	}
	if s.FilesChanged != nil {
		out["files_changed"] = *s.FilesChanged
	}
	if s.IndexedCommit != "" {
		out["indexed_commit"] = s.IndexedCommit
	}
	if !s.IndexedAt.IsZero() {
		out["indexed_at"] = s.IndexedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// axiStalenessNotApplicable is memory-search's `stale` object.
func axiStalenessNotApplicable() map[string]any {
	return map[string]any{
		"state":  stalenessNotApplicable,
		"reason": "memory_not_indexed", // memories are stored, not indexed from the working tree
	}
}

// stalenessLine is the sentence plain text and stderr print, and "" for a fresh
// index.
func stalenessLine(s semantic.Staleness) string {
	switch s.State {
	case semantic.StalenessFresh:
		return ""
	case semantic.StalenessStale:
		return fmt.Sprintf("Index is stale: %s behind HEAD, %s changed%s. Run `llm-semantic index <path>` to bring it up to date.",
			plural(deref(s.CommitsBehind), "commit"), plural(deref(s.FilesChanged), "uncommitted file"), indexedWhen(s))
	}
	return fmt.Sprintf("Index staleness unknown: %s%s, so these results may not match the working tree.",
		unknownStalenessReason(s), indexedWhen(s))
}

func unknownStalenessReason(s semantic.Staleness) string {
	var why string
	switch s.Reason {
	case semantic.StalenessReasonNoRecord:
		why = "no last index is recorded (it was built before the record existed; re-index to record it)"
	case semantic.StalenessReasonUnsupported:
		why = "the storage backend does not record the last index"
	case semantic.StalenessReasonReadFailed:
		why = "the record of the last index could not be read"
	case semantic.StalenessReasonNotGit:
		why = "the directory is not in a git repository"
	case semantic.StalenessReasonNoCommit:
		why = "the index was built outside git, so there is no commit to compare"
	case semantic.StalenessReasonNoHead:
		why = "the repository has no commits"
	case semantic.StalenessReasonCommitUnknown:
		why = "the indexed commit is not in the history of HEAD (a rebase or a shallow clone)"
	case semantic.StalenessReasonGitFailed:
		why = "git could not measure it"
	default:
		why = "the staleness could not be measured"
	}
	if s.Detail != "" {
		why += " (" + s.Detail + ")"
	}
	return why
}

// indexedWhen reads " (indexed at <commit> on <time>)", with whichever parts were
// recorded.
func indexedWhen(s semantic.Staleness) string {
	commit := s.IndexedCommit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	switch {
	case commit != "" && !s.IndexedAt.IsZero():
		return fmt.Sprintf(" (indexed at %s on %s)", commit, s.IndexedAt.UTC().Format(time.RFC3339))
	case commit != "":
		return fmt.Sprintf(" (indexed at %s)", commit)
	case !s.IndexedAt.IsZero():
		return fmt.Sprintf(" (indexed on %s)", s.IndexedAt.UTC().Format(time.RFC3339))
	}
	return ""
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func deref(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}

// printStalenessLine writes the staleness line, if the index is not fresh, to
// stderr for --json and --min, or to stdout after plain text output.
func printStalenessLine(s semantic.Staleness, toStderr bool) {
	if toStderr {
		if line := stalenessLine(s); line != "" {
			fmt.Fprintln(os.Stderr, line)
		}
		return
	}
	writeStalenessLine(os.Stdout, &s)
}

// writeStalenessLine ends plain output with the staleness line, set off by a
// blank line, unless the index is fresh or the caller did not measure it.
func writeStalenessLine(w io.Writer, stale *semantic.Staleness) {
	if stale == nil {
		return
	}
	if line := stalenessLine(*stale); line != "" {
		fmt.Fprintf(w, "\n%s\n", line)
	}
}
