package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic/commands"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
		// TOON is the default (epic 27 T1), so an error with no format flag,
		// or with --axi=false, is TOON too; a --plain after the "--"
		// terminator is a positional argument, not a format request.
		"no format flag":        {"search", "--nope"},
		"--axi=false":           {"search", "--nope", "--axi=false"},
		"--plain after the end": {"search", "--nope", "--", "--plain"},
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

func TestMain_FlagParseErrorUnderPlainIsPlainOnStdout(t *testing.T) {
	// Defends the plain half of epic 27 T2: under --plain a flag-parse error is
	// the plain "Error: ..." on stdout with stderr empty. Until T2 it went to
	// stderr and left stdout empty. An --axi after a "--" terminator is a
	// positional argument and must not switch it to TOON.
	cases := map[string][]string{
		"--plain":             {"search", "--nope", "--plain"},
		"--plain --axi=false": {"search", "--nope", "--plain", "--axi=false"},
		"after terminator":    {"search", "--nope", "--plain", "--", "--axi"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, code := runMain(t, args...)

			if code == 0 {
				t.Errorf("exit code 0 for a flag-parse error; stdout=%q", stdout)
			}
			if !strings.HasPrefix(stdout, "Error:") || !strings.Contains(stdout, "unknown") {
				t.Errorf("stdout lacks the plain error: %q", stdout)
			}
			if stderr != "" {
				t.Errorf("stderr should be empty under --plain: %q", stderr)
			}
		})
	}
}

func TestMain_UnknownFlagListsTheValidFlagsOnTheRightStream(t *testing.T) {
	// Defends AXI principle 6 end to end: an unknown flag must be named, the
	// command's flags listed with a pointer to --help, and the run must exit 2.
	// Under --axi it is TOON on stdout with help[]; under --plain it is plain
	// text, also on stdout (epic 27 T2), with stderr empty in both.
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

		t.Run(sub+" --plain", func(t *testing.T) {
			stdout, stderr, code := runMain(t, sub, "x", "--bogus", "--plain")
			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			for _, want := range wants {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout lacks %q:\n%s", want, stdout)
				}
			}
			if stderr != "" {
				t.Errorf("stderr should be empty in plain mode: %q", stderr)
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

func TestMain_ArgumentCountErrorsExitTwoOnTheRequestedStream(t *testing.T) {
	// Defends epic review finding F1: a wrong number of positional arguments is
	// a malformed invocation, but it exited 1, and under --json it printed
	// nothing on stdout. Every subcommand declares its own local --json, which
	// shadows the root flag, and cobra rejects the arguments before
	// PersistentPreRunE would have copied it to GlobalJSONOutput.
	cases := map[string][]string{
		"search, too few": {"search"},
		"refs, too few":   {"refs"},
		"index, too many": {"index", "a", "b"},
	}
	for name, args := range cases {
		t.Run(name+" --plain", func(t *testing.T) {
			stdout, stderr, code := runMain(t, append(args, "--plain")...)
			if code != 2 {
				t.Errorf("exit code = %d, want 2; stderr=%q", code, stderr)
			}
			if stderr != "" || !strings.HasPrefix(stdout, "Error:") || !strings.Contains(stdout, "arg(s)") {
				t.Errorf("plain error should be on stdout only: stdout=%q stderr=%q", stdout, stderr)
			}
		})

		t.Run(name+" --json", func(t *testing.T) {
			stdout, stderr, code := runMain(t, append(args, "--json")...)
			if code != 2 {
				t.Errorf("exit code = %d, want 2; stdout=%q stderr=%q", code, stdout, stderr)
			}
			var got struct {
				Error   bool   `json:"error"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(stdout), &got); err != nil {
				t.Fatalf("stdout is not a JSON error: %v; stdout=%q stderr=%q", err, stdout, stderr)
			}
			if !got.Error || !strings.Contains(got.Message, "arg(s)") {
				t.Errorf("stdout JSON = %+v, want error: true naming the argument count", got)
			}
			if stderr != "" {
				t.Errorf("stderr should be empty under --json: %q", stderr)
			}
		})

		t.Run(name+" --json --min", func(t *testing.T) {
			stdout, _, code := runMain(t, append(args, "--json", "--min")...)
			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if !strings.Contains(stdout, `"err":true`) {
				t.Errorf("stdout carries no minimal JSON error: %q", stdout)
			}
		})

		t.Run(name+" --axi", func(t *testing.T) {
			stdout, _, code := runMain(t, append(args, "--axi")...)
			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if !strings.Contains(stdout, "error: true") {
				t.Errorf("stdout carries no TOON error: %q", stdout)
			}
		})
	}
}

func TestMain_FlagParseErrorUnderJSONIsJSONOnStdout(t *testing.T) {
	// The --json twin of TestMain_FlagParseErrorUnderAXIIsTOONOnStdout: an
	// unknown flag stops parsing before --json is bound, so it must be read
	// from the arguments. --json=false and a "--" terminator do not ask for
	// JSON, so they get the default, TOON.
	stdout, _, code := runMain(t, "search", "--nope", "--json")
	if code != 2 || !strings.Contains(stdout, `"error": true`) {
		t.Errorf("exit %d, stdout=%q; want 2 and a JSON error", code, stdout)
	}
	for _, args := range [][]string{{"search", "--nope", "--json=false"}, {"search", "--nope", "--", "--json"}} {
		if stdout, _, _ := runMain(t, args...); strings.Contains(stdout, `"error"`) || !strings.Contains(stdout, "error: true") {
			t.Errorf("%v: stdout should be a TOON error, not JSON: %q", args, stdout)
		}
	}
}

// everyCommandPath lists the argument path to every command RootCmd builds,
// root included, so a command added later is covered without editing a test.
func everyCommandPath(c *cobra.Command) [][]string {
	path := strings.Fields(c.CommandPath())[1:]
	var required []string
	c.Flags().VisitAll(func(f *pflag.Flag) {
		if _, ok := f.Annotations[cobra.BashCompOneRequiredFlag]; ok {
			required = append(required, "--"+f.Name, "x")
		}
	})
	paths := [][]string{append(path, required...)}
	for _, sub := range c.Commands() {
		if sub.Name() != "help" && sub.Name() != "completion" {
			paths = append(paths, everyCommandPath(sub)...)
		}
	}
	return paths
}

func TestMain_ContradictoryFormatFlagsExitTwoWithTheErrorOnStdout(t *testing.T) {
	// Defends epic 27 T1 at the process boundary, for every command: a
	// contradictory set of format flags exits 2 with the error on stdout and
	// nothing on stderr. The check fails before any format is resolved, so
	// main() must still pick one from the arguments; it reports in the
	// default, TOON, whichever flag came first. A process that exited 2 with
	// an empty stdout would leave an agent only the exit code to go on.
	rejected := [][]string{{"--plain", "--json"}, {"--plain", "--axi"}, {"--json", "--axi"}, {"--plain", "--min"}}
	for _, path := range everyCommandPath(commands.RootCmd()) {
		for _, flags := range rejected {
			args := append(append([]string{}, path...), flags...)
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				stdout, stderr, code := runMain(t, args...)
				if code != 2 {
					t.Errorf("exit code = %d, want 2; stdout=%q stderr=%q", code, stdout, stderr)
				}
				if !strings.Contains(stdout, "error: true") || !strings.Contains(stdout, "cannot be combined") {
					t.Errorf("stdout carries no TOON format-conflict error: %q", stdout)
				}
				if stderr != "" {
					t.Errorf("stderr should be empty: %q", stderr)
				}
			})
		}
	}
}

func TestMain_ErrorsGoToStdoutInTheActiveFormat(t *testing.T) {
	// Defends epic 27 T2 (AXI principle 6): every error, in every output
	// format, is printed on stdout in that format, with nothing on stderr and
	// the exit code unchanged. Plain mode used to divert its "Error: ..." to
	// stderr, so a caller reading stdout got an empty stream and had only the
	// exit code to learn that the command failed. An unknown flag stops
	// parsing before the format flag is bound, so it is tried on both sides
	// of the format flag; the result must not depend on argument order.
	formats := []struct {
		name  string
		flags []string
		// isError reports whether stdout is an error in this format.
		isError func(stdout string) bool
	}{
		{"default TOON", nil, isTOONError},
		{"--axi", []string{"--axi"}, isTOONError},
		{"--plain", []string{"--plain"}, func(s string) bool { return strings.HasPrefix(s, "Error: ") }},
		{"--json", []string{"--json"}, func(s string) bool {
			var got struct {
				Error   bool   `json:"error"`
				Message string `json:"message"`
			}
			return json.Unmarshal([]byte(s), &got) == nil && got.Error && got.Message != ""
		}},
		{"--json --min", []string{"--json", "--min"}, func(s string) bool {
			var got struct {
				Err bool   `json:"err"`
				Msg string `json:"msg"`
			}
			return json.Unmarshal([]byte(s), &got) == nil && got.Err && got.Msg != ""
		}},
	}
	cases := []struct {
		name string
		args func(format []string) []string
		code int
		// names is a fragment the error must carry, so a test cannot pass on
		// some other error that happens to share the exit code.
		names string
	}{
		{"unknown flag before the format", func(f []string) []string { return append([]string{"search", "x", "--nope"}, f...) }, 2, "--nope"},
		{"unknown flag after the format", func(f []string) []string { return append(append([]string{"search", "x"}, f...), "--nope") }, 2, "--nope"},
		{"unknown command", func(f []string) []string { return append([]string{"nosuchcmd"}, f...) }, 2, "nosuchcmd"},
		{"runtime error", func(f []string) []string {
			return append([]string{"search", "foo", "--index-dir", "nope"}, f...)
		}, 1, "index"},
	}
	for _, format := range formats {
		for _, c := range cases {
			t.Run(format.name+"/"+c.name, func(t *testing.T) {
				stdout, stderr, code := runMain(t, c.args(format.flags)...)
				if code != c.code {
					t.Errorf("exit code = %d, want %d; stdout=%q stderr=%q", code, c.code, stdout, stderr)
				}
				if !format.isError(stdout) || !strings.Contains(stdout, c.names) {
					t.Errorf("stdout does not hold a %s error naming %q: stdout=%q stderr=%q", format.name, c.names, stdout, stderr)
				}
				if stderr != "" {
					t.Errorf("stderr should be empty: %q", stderr)
				}
			})
		}
	}
}

// isTOONError reports whether s is a TOON error object.
func isTOONError(s string) bool {
	return strings.HasPrefix(s, "error: true\n") && strings.Contains(s, "message:")
}
