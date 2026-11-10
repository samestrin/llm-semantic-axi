package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
)

// Epic 04 T4. A search over an index built from older code read exactly like a
// search over a current one: results, exit 0, nothing to say they may describe
// code that no longer exists. Every search command now carries how stale the
// index is: a `stale` object under --axi, a line in plain text, and one stderr
// line under --json and --min, whose stdout schema stays as it was (board
// decision, 2026-10-31). The search itself still returns its results.

const (
	indexFresh  = "fresh"
	indexStale  = "stale"
	indexNotGit = "not-git"
)

// runGit runs git in dir with an identity and no signing, so a developer's own
// git config cannot fail the commit.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// stalenessWorkspace indexes callGraphSource in a fresh repository, a repository
// one commit ahead of its index, or a directory outside git.
func stalenessWorkspace(t *testing.T, kind string) {
	t.Helper()
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	// Stops git finding a repository above the temp dir, such as the one this
	// suite runs in.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))

	writeFile(t, filepath.Join(dir, "sample.go"), callGraphSource)
	if kind != indexNotGit {
		runGit(t, dir, "init", "-q")
		runGit(t, dir, "add", "sample.go")
		runGit(t, dir, "commit", "-q", "-m", "sample")
	}
	captureStdout(t, func() {
		if err := runIndex(context.Background(), ".", indexOpts{skipCalibration: true}); err != nil {
			t.Fatalf("runIndex: %v", err)
		}
	})
	if kind == indexStale {
		writeFile(t, filepath.Join(dir, "later.go"), "package sample\n")
		runGit(t, dir, "add", "later.go")
		runGit(t, dir, "commit", "-q", "-m", "later")
	}
}

// captureBoth returns what fn wrote to stdout and to stderr.
func captureBoth(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, fn)
	})
	return stdout, stderr
}

// outputMode is one of the four ways a search command prints.
type outputMode struct {
	name       string
	json, min  bool
	axi        bool
	structured bool // stdout is JSON
}

var outputModes = []outputMode{
	{name: "plain"},
	{name: "json", json: true, structured: true},
	{name: "min", min: true, structured: true},
	{name: "axi", axi: true},
}

// stalenessCommand is a search command under test: run prints, want is the text
// every mode's stdout must still carry, proving the results were returned.
type stalenessCommand struct {
	name string
	run  func(ctx context.Context, m outputMode) error
	want string
	// bareArray: --json and --min print a bare array, which must stay one.
	bareArray bool
}

var stalenessCommands = []stalenessCommand{
	{
		name: "search",
		run: func(ctx context.Context, m outputMode) error {
			return runSearch(ctx, "authenticate token", searchOpts{topK: 5, jsonOutput: m.json, minOutput: m.min})
		},
		want:      "Authenticate",
		bareArray: true,
	},
	{
		name: "code-search alias",
		run: func(ctx context.Context, m outputMode) error {
			cmd := searchAliasCmd("code-search", "code")
			cmd.SetContext(ctx)
			if m.json {
				_ = cmd.Flags().Set("json", "true")
			}
			if m.min {
				_ = cmd.Flags().Set("min", "true")
			}
			return cmd.RunE(cmd, []string{"authenticate", "token"})
		},
		want:      "Authenticate",
		bareArray: true,
	},
	{
		name: "multisearch",
		run: func(ctx context.Context, m outputMode) error {
			return runMultisearch(ctx, []string{"authenticate token", "caller"}, multisearchOpts{topK: 5, jsonOutput: m.json, minOutput: m.min})
		},
		want: "Authenticate",
	},
	{
		name: "callers",
		run: func(ctx context.Context, m outputMode) error {
			return runCallers(ctx, "Authenticate", m.json, m.min)
		},
		want: "Caller",
	},
	{
		name: "refs",
		run: func(ctx context.Context, m outputMode) error {
			return runRefs(ctx, "Caller", m.json, m.min)
		},
		want: "Authenticate",
	},
}

// TestSearchCommandsCarryTheStaleness is the task's "done when": every search
// command, in every output mode, over a stale, a fresh and a non-git index.
func TestSearchCommandsCarryTheStaleness(t *testing.T) {
	for _, c := range stalenessCommands {
		for _, kind := range []string{indexStale, indexFresh, indexNotGit} {
			for _, m := range outputModes {
				t.Run(c.name+"/"+kind+"/"+m.name, func(t *testing.T) {
					stalenessWorkspace(t, kind)
					if m.axi {
						withAXI(t)
					}

					stdout, stderr := captureBoth(t, func() {
						if err := c.run(context.Background(), m); err != nil {
							t.Fatalf("%s: %v", c.name, err)
						}
					})

					// The search still returns its results, stale or not.
					if !strings.Contains(stdout, c.want) {
						t.Errorf("results missing: stdout lacks %q:\n%s", c.want, stdout)
					}

					switch {
					case m.axi:
						assertAXIStaleness(t, kind, stdout)
						if stderr != "" {
							t.Errorf("--axi wrote to stderr; the staleness belongs in the payload:\n%s", stderr)
						}
					case m.structured:
						assertSchemaUnchanged(t, c, stdout)
						assertStalenessLine(t, kind, stderr, "stderr")
					default:
						assertStalenessLine(t, kind, stdout, "plain stdout")
						if stderr != "" {
							t.Errorf("plain output wrote to stderr:\n%s", stderr)
						}
					}
				})
			}
		}
	}
}

// assertAXIStaleness checks the `stale` object --axi carries.
func assertAXIStaleness(t *testing.T, kind, stdout string) {
	t.Helper()
	if !strings.Contains(stdout, "stale:") {
		t.Fatalf("--axi carries no stale object:\n%s", stdout)
	}
	var want []string
	switch kind {
	case indexStale:
		want = []string{"state: stale", "commits_behind: 1", "files_changed: 0", "indexed_commit:", "indexed_at:"}
	case indexFresh:
		want = []string{"state: fresh", "commits_behind: 0", "files_changed: 0"}
	case indexNotGit:
		want = []string{"state: unknown", "reason: not_git"}
	}
	for _, w := range want {
		if !strings.Contains(stdout, w) {
			t.Errorf("--axi stale object lacks %q:\n%s", w, stdout)
		}
	}
	if kind == indexNotGit && strings.Contains(stdout, "commits_behind") {
		t.Errorf("an unknown staleness carries a count, which reads as measured:\n%s", stdout)
	}
}

// assertStalenessLine checks the one line plain text and stderr carry: what is
// behind when stale, why it is unknown outside git, and nothing when fresh.
func assertStalenessLine(t *testing.T, kind, out, where string) {
	t.Helper()
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "Index is stale") || strings.Contains(l, "Index staleness") {
			lines = append(lines, l)
		}
	}
	switch kind {
	case indexFresh:
		if len(lines) != 0 {
			t.Errorf("%s reports a fresh index as stale or unknown: %q", where, lines)
		}
		return
	case indexStale:
		if len(lines) != 1 || !strings.Contains(lines[0], "1 commit behind") || !strings.Contains(lines[0], "0 uncommitted files changed") {
			t.Errorf("%s: want one line saying the index is 1 commit behind, got %q in:\n%s", where, lines, out)
		}
	case indexNotGit:
		if len(lines) != 1 || !strings.Contains(lines[0], "unknown") || !strings.Contains(lines[0], "not in a git repository") {
			t.Errorf("%s: want one line saying the staleness is unknown outside git, got %q in:\n%s", where, lines, out)
		}
	}
	if where == "stderr" && strings.Count(strings.TrimRight(out, "\n"), "\n") != 0 {
		t.Errorf("stderr carries more than one line:\n%s", out)
	}
}

// assertSchemaUnchanged checks --json and --min stdout gained nothing: still
// JSON, still a bare array where it was one, and no staleness key.
func assertSchemaUnchanged(t *testing.T, c stalenessCommand, stdout string) {
	t.Helper()
	if c.bareArray {
		var rows []json.RawMessage
		decodeJSON(t, stdout, &rows)
		if len(rows) == 0 {
			t.Errorf("--json returned an empty array:\n%s", stdout)
		}
	} else {
		var obj map[string]json.RawMessage
		decodeJSON(t, stdout, &obj)
		for _, key := range []string{"stale", "staleness"} {
			if _, ok := obj[key]; ok {
				t.Errorf("--json gained a %q key; its schema must not change:\n%s", key, stdout)
			}
		}
	}
	if strings.Contains(stdout, "commits_behind") || strings.Contains(stdout, "not_git") {
		t.Errorf("--json stdout carries the staleness; it belongs on stderr:\n%s", stdout)
	}
}

// TestMemorySearchSaysStalenessIsNotApplicable: memories are stored, not indexed
// from the working tree, so there is nothing to be stale against. --axi says so
// rather than leaving the key out, which an agent could read as "not checked";
// the other modes print nothing extra.
func TestMemorySearchSaysStalenessIsNotApplicable(t *testing.T) {
	for _, m := range outputModes {
		t.Run(m.name, func(t *testing.T) {
			newWorkspace(t)
			useEmbedder(t, newFakeEmbedder(t))
			seedMemories(t, newMemory("mem-1", "How are tokens authenticated?", "With a bearer token."))
			if m.axi {
				withAXI(t)
			}

			stdout, stderr := captureBoth(t, func() {
				if err := runMemorySearch(context.Background(), memorySearchOpts{
					query: "authenticated tokens", topK: 5, jsonOutput: m.json, minOutput: m.min,
				}); err != nil {
					t.Fatalf("runMemorySearch: %v", err)
				}
			})

			if !strings.Contains(stdout, "mem-1") {
				t.Errorf("results missing:\n%s", stdout)
			}
			if stderr != "" {
				t.Errorf("memory-search wrote to stderr:\n%s", stderr)
			}
			if m.axi {
				for _, w := range []string{"stale:", "state: not_applicable"} {
					if !strings.Contains(stdout, w) {
						t.Errorf("--axi lacks %q:\n%s", w, stdout)
					}
				}
				return
			}
			if strings.Contains(stdout, "stale") || strings.Contains(stdout, "Index staleness") {
				t.Errorf("%s memory-search output gained a staleness note:\n%s", m.name, stdout)
			}
			if m.json {
				var rows []json.RawMessage
				decodeJSON(t, stdout, &rows)
			}
		})
	}
}

// withoutStalenessNote strips the staleness note that ends plain output, so a
// test pinning the text before it byte for byte keeps doing so. These workspaces
// sit outside git, so the note must be there, and must be the last line alone.
func withoutStalenessNote(t *testing.T, out string) string {
	t.Helper()
	i := strings.LastIndex(out, "\nIndex staleness unknown: ")
	if i < 0 {
		t.Fatalf("plain output outside git lacks the staleness note:\n%s", out)
	}
	if note := out[i+1:]; strings.Count(note, "\n") != 1 || !strings.HasSuffix(note, "\n") {
		t.Fatalf("the staleness note is not the last line alone:\n%s", out)
	}
	return out[:i]
}

// TestStalenessLineNamesEveryUnknownReason defends against an unknown staleness
// reading as fresh: every reason prints a line that says "unknown" and why, never
// an empty line, which is what fresh prints.
func TestStalenessLineNamesEveryUnknownReason(t *testing.T) {
	cases := map[string]string{
		semantic.StalenessReasonNoRecord:      "no last index is recorded",
		semantic.StalenessReasonUnsupported:   "does not record the last index",
		semantic.StalenessReasonReadFailed:    "could not be read",
		semantic.StalenessReasonNotGit:        "not in a git repository",
		semantic.StalenessReasonNoCommit:      "built outside git",
		semantic.StalenessReasonNoHead:        "has no commits",
		semantic.StalenessReasonCommitUnknown: "not in the history of HEAD",
		semantic.StalenessReasonGitFailed:     "git could not measure it",
		"something_new":                       "could not be measured",
	}
	for reason, want := range cases {
		line := stalenessLine(semantic.Staleness{State: semantic.StalenessUnknown, Reason: reason})
		if !strings.HasPrefix(line, "Index staleness unknown: ") || !strings.Contains(line, want) {
			t.Errorf("reason %s: line %q lacks %q", reason, line, want)
		}
	}

	line := stalenessLine(semantic.Staleness{State: semantic.StalenessUnknown, Reason: semantic.StalenessReasonGitFailed, Detail: "exit status 128"})
	if !strings.Contains(line, "(exit status 128)") {
		t.Errorf("the git failure's detail is dropped: %q", line)
	}
	commit := strings.Repeat("a", 40)
	line = stalenessLine(semantic.Staleness{State: semantic.StalenessUnknown, Reason: semantic.StalenessReasonCommitUnknown, IndexedCommit: commit})
	if !strings.Contains(line, "(indexed at aaaaaaaaaaaa)") || strings.Contains(line, commit) {
		t.Errorf("a commit with no time should read as its short form alone: %q", line)
	}
}

// TestAXIStalenessCarriesTheDetail: a git failure under --axi says what failed,
// as plain text does.
func TestAXIStalenessCarriesTheDetail(t *testing.T) {
	obj := axiStaleness(semantic.Staleness{State: semantic.StalenessUnknown, Reason: semantic.StalenessReasonGitFailed, Detail: "boom"})
	if obj["detail"] != "boom" || obj["reason"] != semantic.StalenessReasonGitFailed {
		t.Errorf("axiStaleness = %v", obj)
	}
	if _, ok := obj["indexed_at"]; ok {
		t.Errorf("an unrecorded index time is printed: %v", obj)
	}
}

// refsOnly is a RefStorage that is not a Storage.
type refsOnly struct{ semantic.RefStorage }

// TestRefStalenessWithoutAStorageIsUnknown defends against a call graph view the
// staleness cannot be read from reporting fresh.
func TestRefStalenessWithoutAStorageIsUnknown(t *testing.T) {
	s := refStaleness(context.Background(), refsOnly{})
	if s.State != semantic.StalenessUnknown || s.Reason != semantic.StalenessReasonUnsupported {
		t.Errorf("refStaleness = %+v, want unknown/unsupported", s)
	}
}

// lastIndexOnly is a Storage that has recorded a last index, so CheckStaleness
// goes on to run git.
type lastIndexOnly struct{ semantic.Storage }

func (lastIndexOnly) GetLastIndex(context.Context) (*semantic.LastIndex, error) {
	return &semantic.LastIndex{Commit: strings.Repeat("a", 40), IndexedAt: time.Now()}, nil
}

// TestIndexStalenessHasItsOwnTimeout defends against a hung git (a lock, a slow
// network filesystem) holding a search forever: the search commands pass a
// context with no deadline, so the staleness check must bring its own.
func TestIndexStalenessHasItsOwnTimeout(t *testing.T) {
	old := stalenessTimeout
	stalenessTimeout = 100 * time.Millisecond
	t.Cleanup(func() { stalenessTimeout = old })

	// A git that never answers. exec makes sleep the process the timeout kills,
	// so its stdout pipe closes with it.
	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nexec sleep 30\n")
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	// No deadline; cancelled only if the check overruns, so a failing run does
	// not leave the stub sleeping.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan semantic.Staleness, 1)
	start := time.Now()
	go func() { done <- indexStaleness(ctx, lastIndexOnly{}) }()

	select {
	case s := <-done:
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("indexStaleness took %v with a %v timeout", elapsed, stalenessTimeout)
		}
		if s.State != semantic.StalenessUnknown || s.Reason != semantic.StalenessReasonGitFailed {
			t.Errorf("indexStaleness = %+v, want unknown/git_failed", s)
		}
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatalf("indexStaleness still running after 5s with a %v timeout", stalenessTimeout)
	}
}

// TestStalenessWithAnIndexDirMeasuresTheIndexedRepository defends `--index-dir`
// pointing at another repository's index: callers run from repository B over
// the index of repository A must report how far A has moved, not
// unknown/commit_unknown because A's commit is not in B's history.
func TestStalenessWithAnIndexDirMeasuresTheIndexedRepository(t *testing.T) {
	here := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(here))
	writeFile(t, filepath.Join(here, "b.go"), "package b\n")
	runGit(t, here, "init", "-q")
	runGit(t, here, "add", "b.go")
	runGit(t, here, "commit", "-q", "-m", "b")

	indexed := t.TempDir()
	writeFile(t, filepath.Join(indexed, "sample.go"), callGraphSource)
	runGit(t, indexed, "init", "-q")
	runGit(t, indexed, "add", "sample.go")
	runGit(t, indexed, "commit", "-q", "-m", "sample")

	indexDir = filepath.Join(t.TempDir(), "idx")
	captureStdout(t, func() {
		if err := runIndex(context.Background(), indexed, indexOpts{skipCalibration: true}); err != nil {
			t.Fatalf("runIndex: %v", err)
		}
	})
	writeFile(t, filepath.Join(indexed, "later.go"), "package sample\n")
	runGit(t, indexed, "add", "later.go")
	runGit(t, indexed, "commit", "-q", "-m", "later")

	withAXI(t)
	out := captureStdout(t, func() {
		if err := runCallers(context.Background(), "Authenticate", false, false); err != nil {
			t.Fatalf("callers: %v", err)
		}
	})
	if !strings.Contains(out, "Caller") {
		t.Fatalf("callers did not answer from the index of the other repository:\n%s", out)
	}
	for _, want := range []string{"state: stale", "commits_behind: 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("callers over another repository's index lacks %q:\n%s", want, out)
		}
	}
}
