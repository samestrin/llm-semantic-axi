package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic/commands"
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

func TestMain_VersionFlagsPrintTheBareVersion(t *testing.T) {
	// --version, -v and -V must each print exactly the stamped version and a
	// newline on stdout, nothing on stderr, and exit 0. The commit used to be
	// appended, which broke `$(llm-semantic -v)` in scripts.
	for _, flag := range []string{"-v", "-V", "--version"} {
		t.Run(flag, func(t *testing.T) {
			stdout, stderr, code := runMain(t, flag)

			if stdout != version+"\n" {
				t.Errorf("stdout = %q, want %q", stdout, version+"\n")
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}
			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
		})
	}
}

func TestMain_UnstampedBuildReportsDev(t *testing.T) {
	// An unstamped build must say "dev", not an empty string or a fake number.
	if version != "dev" {
		t.Skip("binary was stamped with ldflags")
	}
	stdout, _, _ := runMain(t, "--version")
	if stdout != "dev\n" {
		t.Errorf("stdout = %q, want \"dev\\n\"", stdout)
	}
}

func TestMain_IndexDashVStillMeansVerbose(t *testing.T) {
	// -v on a subcommand is --verbose. It must not print the version, and must
	// not be rejected as an unknown flag.
	stdout, stderr, _ := runMain(t, "index", "-v", "--help")
	if strings.Contains(stderr, "unknown") || stdout == version+"\n" {
		t.Errorf("index -v mishandled: stdout=%q stderr=%q", stdout, stderr)
	}
	root := commands.RootCmd()
	idx, _, err := root.Find([]string{"index"})
	if err != nil {
		t.Fatalf("Find index: %v", err)
	}
	if f := idx.Flags().ShorthandLookup("v"); f == nil || f.Name != "verbose" {
		t.Errorf("index -v does not map to --verbose: %v", f)
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

func TestMain_FlagParseErrorUnderAXIIsTOONOnStdout(t *testing.T) {
	// Defends the same contract as TestMain_AXIErrorIsTOONOnStdout for the
	// failure that never reaches PersistentPreRunE: an unknown flag fails in
	// cobra's flag parsing, so --axi was never published to the output package
	// and the agent that asked for TOON got an empty stdout and a plain stderr.
	cases := map[string][]string{
		"unknown flag after --axi":  {"search", "--axi", "--nope"},
		"unknown flag before --axi": {"search", "--nope", "--axi"},
		"unknown subcommand":        {"nosuchcmd", "--axi"},
		"explicit --axi=true":       {"search", "--nope", "--axi=true"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, code := runMain(t, args...)

			if code == 0 {
				t.Errorf("exit code 0 for a flag-parse error; stdout=%q stderr=%q", stdout, stderr)
			}
			if !strings.Contains(stdout, "error: true") || !strings.Contains(stdout, "message:") {
				t.Errorf("stdout carries no TOON error: stdout=%q stderr=%q", stdout, stderr)
			}
			if strings.Contains(stderr, "Error:") {
				t.Errorf("the error went to stderr as plain text instead: %q", stderr)
			}
		})
	}
}

func TestMain_FlagParseErrorWithoutAXIIsUnchanged(t *testing.T) {
	// Defends the other half: without --axi (or with --axi=false, or after a
	// "--" terminator) the plain "Error:" on stderr and empty stdout must stay.
	cases := map[string][]string{
		"no --axi":         {"search", "--nope"},
		"--axi=false":      {"search", "--nope", "--axi=false"},
		"after terminator": {"search", "--nope", "--", "--axi"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, code := runMain(t, args...)

			if code == 0 {
				t.Errorf("exit code 0 for a flag-parse error; stdout=%q", stdout)
			}
			if stdout != "" {
				t.Errorf("stdout should be empty without --axi: %q", stdout)
			}
			if !strings.Contains(stderr, "Error:") || !strings.Contains(stderr, "unknown") {
				t.Errorf("stderr lost the plain error: %q", stderr)
			}
		})
	}
}

func TestMain_UnknownFlagListsTheValidFlagsOnTheRightStream(t *testing.T) {
	// Defends AXI principle 6 end to end: an unknown flag must be named, the
	// command's flags listed with a pointer to --help, and the run must exit 2.
	// Under --axi it is TOON on stdout with help[]; plain mode keeps stderr and
	// leaves stdout empty, the documented exception.
	cases := map[string]string{"search": "--top", "index": "--include"}
	for sub, local := range cases {
		wants := []string{"--bogus", "--axi", "--json", "--min", local, "llm-semantic " + sub + " --help"}

		t.Run(sub+" --axi", func(t *testing.T) {
			stdout, stderr, code := runMain(t, sub, "x", "--bogus", "--axi")
			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			for _, want := range wants {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout lacks %q:\n%s", want, stdout)
				}
			}
			if !strings.Contains(stdout, "help[") {
				t.Errorf("stdout carries no help[]:\n%s", stdout)
			}
			if stderr != "" {
				t.Errorf("stderr should be empty under --axi: %q", stderr)
			}
		})

		t.Run(sub+" plain", func(t *testing.T) {
			stdout, stderr, code := runMain(t, sub, "x", "--bogus")
			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			for _, want := range wants {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr lacks %q:\n%s", want, stderr)
				}
			}
			if stdout != "" {
				t.Errorf("stdout should be empty in plain mode: %q", stdout)
			}
		})
	}
}

func TestMain_UnknownSubcommandUnderAXICarriesHelp(t *testing.T) {
	// The unknown-command half: named, valid commands listed, help[] on stdout,
	// exit 2. `memory bogus` used to print help and exit 0.
	for _, args := range [][]string{{"bogus", "--axi"}, {"memory", "bogus", "--axi"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, code := runMain(t, args...)
			if code != 2 {
				t.Errorf("exit code = %d, want 2; stdout=%q stderr=%q", code, stdout, stderr)
			}
			if !strings.Contains(stdout, `bogus`) || !strings.Contains(stdout, "help[") {
				t.Errorf("stdout does not name the command and carry help[]:\n%s", stdout)
			}
		})
	}
}
