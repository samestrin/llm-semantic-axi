package commands

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
