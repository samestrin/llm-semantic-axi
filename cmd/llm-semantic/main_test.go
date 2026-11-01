package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// main() is thin, but the one thing it does that nothing else does is stamp the
// build-time version/commit onto the root command. That stamping is how a bug
// report says which binary produced it, and it is exactly what silently broke
// while this package lived inside llm-tools (a hardcoded "1.5.0").

// captureStdout runs fn with os.Stdout redirected and returns what was written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	realStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = realStdout }()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatalf("Close reader: %v", err)
	}
	return out
}

// withArgs swaps os.Args for the duration of the test.
func withArgs(t *testing.T, args ...string) {
	t.Helper()
	original := os.Args
	os.Args = args
	t.Cleanup(func() { os.Args = original })
}

func TestMain_StampsTheVersionOntoTheRootCommand(t *testing.T) {
	// --version is answered by cobra from rootCmd.Version, which only main()
	// sets. If the stamping were dropped the flag would disappear entirely
	// rather than report a wrong number, so this pins both.
	withArgs(t, "llm-semantic", "--version")

	out := captureStdout(t, main)

	if !strings.Contains(out, version) || !strings.Contains(out, commit) {
		t.Errorf("--version output %q carries neither the version %q nor the commit %q", out, version, commit)
	}
	// The default build reports the ldflags placeholders, formatted as
	// "<version> (<commit>)".
	if !strings.Contains(out, version+" ("+commit+")") {
		t.Errorf("--version output %q is not in the \"<version> (<commit>)\" shape", out)
	}
	if !strings.Contains(out, "llm-semantic") {
		t.Errorf("--version output %q does not name the binary", out)
	}
}

func TestMain_ReturnsWithoutExitingOnASuccessfulRun(t *testing.T) {
	// Execute() returning nil must fall through to a normal return. Calling
	// os.Exit on the success path would kill this test process, so reaching the
	// assertion below is itself the result.
	withArgs(t, "llm-semantic", "--help")

	out := captureStdout(t, main)

	// A runnable root (bare run shows status) adds the "[flags]" usage line.
	if !strings.Contains(out, "Usage:\n  llm-semantic [flags]\n  llm-semantic [command]") {
		t.Errorf("--help did not print the root command's help: %q", out)
	}
	// The subcommands the MCP server shells out to must be reachable from this
	// binary, or every tool call fails with "unknown command".
	for _, sub := range []string{"index", "index-status", "index-update", "search", "multisearch", "memory", "callers", "collection"} {
		if !strings.Contains(out, "\n  "+sub+" ") {
			t.Errorf("subcommand %q is missing from the root help: %q", sub, out)
		}
	}
	// SilenceUsage/SilenceErrors are set on the root command, so help is the
	// only thing on stdout.
	if strings.Contains(out, "Error:") {
		t.Errorf("--help reported an error: %q", out)
	}
}

// mainHelperEnv marks a re-exec of this test binary that should run main() with
// the arguments after "--". main() calls os.Exit on failure, so the only way to
// observe its exit code and both output streams is from a separate process.
const mainHelperEnv = "LLM_SEMANTIC_MAIN_HELPER"

func TestMain_Helper(t *testing.T) {
	if os.Getenv(mainHelperEnv) != "1" {
		t.Skip("only runs as a re-exec from runMain")
	}
	for i, a := range os.Args {
		if a == "--" {
			os.Args = append([]string{"llm-semantic"}, os.Args[i+1:]...)
			break
		}
	}
	main()
	os.Exit(0)
}

// runMain runs main() in a child process inside an empty directory, so no
// index or config file from this checkout is discovered.
func runMain(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestMain_Helper$", "--"}, args...)...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), mainHelperEnv+"=1")
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("running main: %v", err)
	}
	return out.String(), errOut.String(), cmd.ProcessState.ExitCode()
}

func TestMain_AXIErrorIsTOONOnStdout(t *testing.T) {
	// Defends technical-debt row 6b: with --axi, a failed command printed a
	// plain "Error: ..." on stderr and left stdout empty, because main() built
	// its error formatter before anything had published --axi to the output
	// package. An agent parsing TOON from stdout saw no output at all and had
	// only the exit code to learn that the command failed, and why.
	cases := map[string][]string{
		// Fails inside the command: there is no index in the directory.
		"missing index": {"search", "foo", "--axi", "--index-dir", "nope"},
		// Fails inside the root PersistentPreRunE itself, before any command
		// runs, so the flag must be published before that hook can return.
		"invalid config path": {"index-status", "--axi", "--config", "   "},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, code := runMain(t, args...)

			if code == 0 {
				t.Errorf("exit code 0 for a failed command; stdout=%q stderr=%q", stdout, stderr)
			}
			if !strings.Contains(stdout, "error: true") {
				t.Errorf("stdout carries no TOON `error: true`: stdout=%q stderr=%q", stdout, stderr)
			}
			if !strings.Contains(stdout, "message:") {
				t.Errorf("stdout carries no TOON `message`: %q", stdout)
			}
			if strings.Contains(stderr, "Error:") {
				t.Errorf("the error went to stderr as plain text instead: %q", stderr)
			}
		})
	}
}
