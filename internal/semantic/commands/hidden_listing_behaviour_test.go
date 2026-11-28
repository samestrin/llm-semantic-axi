package commands

import (
	"errors"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/v2/pkg/output"
	"github.com/spf13/cobra"
)

// newHiddenListingFixture builds a command tree with one visible flag, one
// hidden flag, one visible subcommand, one hidden subcommand and one
// deprecated subcommand. No committed command declares any of these (the
// epic 41 refine confirmed it), so the filters in flagError and
// unknownCommandError had no test that could see them: debt row
// e7591e24ad368acb records that deleting either filter left the suite green.
// The fixture goes through rejectUnknownCommands and flagError exactly as
// RootCmd wires them, so the tests exercise the real error paths.
func newHiddenListingFixture(t *testing.T) *cobra.Command {
	t.Helper()
	noop := func(cmd *cobra.Command, args []string) error { return nil }
	root := &cobra.Command{Use: "fixture"}
	// Persistent so the flags are in play both at the root and under a
	// subcommand, like the real tree's output flags.
	root.PersistentFlags().Bool("visible-flag", false, "listed in error messages")
	root.PersistentFlags().Bool("secret-flag", false, "never listed")
	if err := root.PersistentFlags().MarkHidden("secret-flag"); err != nil {
		t.Fatalf("MarkHidden: %v", err)
	}
	root.SetFlagErrorFunc(flagError)
	root.AddCommand(
		&cobra.Command{Use: "public", RunE: noop},
		&cobra.Command{Use: "covert", Hidden: true, RunE: noop},
		&cobra.Command{Use: "legacy", Deprecated: "use public instead", RunE: noop},
	)
	rejectUnknownCommands(root)
	// cobra merges persistent flags into Flags() inside ParseFlags, before
	// any FlagErrorFunc runs. Do the same so a direct flagError call sees
	// the flag set it would see in a real invocation.
	if err := root.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	return root
}

// executeFixture runs a fixture tree with args and returns its error.
func executeFixture(t *testing.T, root *cobra.Command, args ...string) error {
	t.Helper()
	root.SetArgs(args)
	var err error
	captureStdout(t, func() { err = root.Execute() })
	return err
}

// TestFlagErrorOmitsHiddenFlagsFromTheValidFlagList defends the `!f.Hidden`
// filter in flagError (axi.go): a hidden flag is hidden on purpose, and
// listing it in the "valid flags" retry hint advertises what the command
// means to keep out of its interface. Deleting the filter must turn this
// test red. The visible flag must still be listed, so hiding everything
// fails too.
func TestFlagErrorOmitsHiddenFlagsFromTheValidFlagList(t *testing.T) {
	cases := map[string]func(t *testing.T) error{
		"direct call": func(t *testing.T) error {
			return flagError(newHiddenListingFixture(t), errors.New("unknown flag: --bogus"))
		},
		"executed": func(t *testing.T) error {
			return executeFixture(t, newHiddenListingFixture(t), "--bogus")
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			err := run(t)
			if err == nil {
				t.Fatal("no error for an unknown flag")
			}
			msg := err.Error()
			for _, want := range []string{"--bogus", "valid flags", "--visible-flag"} {
				if !strings.Contains(msg, want) {
					t.Errorf("message lacks %q:\n%s", want, msg)
				}
			}
			if strings.Contains(msg, "--secret-flag") {
				t.Errorf("message lists the hidden flag:\n%s", msg)
			}
			if code := output.ExitCodeFor(err); code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
		})
	}
}

// TestUnknownCommandErrorOmitsHiddenAndDeprecatedCommands defends the
// `IsAvailableCommand()` filter in unknownCommandError (axi.go): hidden and
// deprecated subcommands must not appear in the "valid commands" retry hint,
// or the error steers callers to commands the tree deliberately keeps out of
// its interface. Deleting the filter must turn this test red. The visible
// command must still be listed, so listing nothing fails too.
func TestUnknownCommandErrorOmitsHiddenAndDeprecatedCommands(t *testing.T) {
	cases := map[string]func(t *testing.T) error{
		"direct call": func(t *testing.T) error {
			return unknownCommandError(newHiddenListingFixture(t), "bogus")
		},
		"executed": func(t *testing.T) error {
			return executeFixture(t, newHiddenListingFixture(t), "bogus")
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			err := run(t)
			if err == nil {
				t.Fatal("no error for an unknown command")
			}
			msg := err.Error()
			for _, want := range []string{`"bogus"`, "valid commands", "public"} {
				if !strings.Contains(msg, want) {
					t.Errorf("message lacks %q:\n%s", want, msg)
				}
			}
			for _, notWant := range []string{"covert", "legacy"} {
				if strings.Contains(msg, notWant) {
					t.Errorf("message lists the hidden or deprecated command %q:\n%s", notWant, msg)
				}
			}
			if code := output.ExitCodeFor(err); code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
		})
	}
}

// TestHiddenFlagAndSubcommandStillWork: hidden means unlisted, not disabled.
// This keeps the guard honest in the other direction — making the listing
// tests pass by rejecting or deleting the hidden flag or subcommand would
// break callers that legitimately use them, and must fail here.
func TestHiddenFlagAndSubcommandStillWork(t *testing.T) {
	if err := executeFixture(t, newHiddenListingFixture(t), "--secret-flag", "public"); err != nil {
		t.Errorf("hidden flag rejected when used: %v", err)
	}
	if err := executeFixture(t, newHiddenListingFixture(t), "covert"); err != nil {
		t.Errorf("hidden subcommand rejected when used: %v", err)
	}
}
