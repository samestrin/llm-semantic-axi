package main

import (
	"bytes"
	"os"
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
