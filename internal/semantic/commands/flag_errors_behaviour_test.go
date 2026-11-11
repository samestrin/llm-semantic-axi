package commands

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/pkg/output"
)

// executeRoot runs a fresh command tree with args and returns its error.
func executeRoot(t *testing.T, args ...string) error {
	t.Helper()
	root := RootCmd()
	root.SetArgs(args)
	var err error
	captureStdout(t, func() { err = root.Execute() })
	return err
}

// axiHelpOf returns the --axi help an error carries, or nil.
func axiHelpOf(err error) []string {
	var h output.AXIHelper
	if errors.As(err, &h) {
		return h.AXIHelp()
	}
	return nil
}

// TestUnknownFlagIsRejectedByNameWithTheValidFlags defends AXI principle 6: an
// unknown flag used to fail with only "unknown flag: --bogus", so an agent
// learned what was wrong but not what it could have typed instead, and had to
// spend a second call on --help. The message must name the flag, list the
// command's flags (inherited and local), point at --help, classify as exit 2,
// and fail before an embedder or the index is touched.
func TestUnknownFlagIsRejectedByNameWithTheValidFlags(t *testing.T) {
	cases := map[string]struct {
		args  []string
		local string
	}{
		"search": {[]string{"search", "foo", "--bogus"}, "--top"},
		"index":  {[]string{"index", ".", "--bogus"}, "--include"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			newWorkspace(t)
			fe := newFakeEmbedder(t)
			useEmbedder(t, fe)

			err := executeRoot(t, tc.args...)
			if err == nil {
				t.Fatal("no error for an unknown flag")
			}
			msg := err.Error()
			for _, want := range []string{"--bogus", "--axi", "--json", "--min", tc.local, "llm-semantic " + name + " --help"} {
				if !strings.Contains(msg, want) {
					t.Errorf("message lacks %q:\n%s", want, msg)
				}
			}
			if code := output.ExitCodeFor(err); code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if help := axiHelpOf(err); len(help) == 0 {
				t.Error("error carries no --axi help")
			}
			if n := fe.callCount(); n != 0 {
				t.Errorf("embedder received %d calls before the flag was rejected", n)
			}
			if _, statErr := os.Stat(".index"); !os.IsNotExist(statErr) {
				t.Errorf("an index directory was created before the flag was rejected: %v", statErr)
			}
		})
	}
}

// TestUnknownShorthandFlagListsTheValidFlags: -Z fails through the same path.
func TestUnknownShorthandFlagListsTheValidFlags(t *testing.T) {
	newWorkspace(t)
	err := executeRoot(t, "search", "foo", "-Z")
	if err == nil {
		t.Fatal("no error for an unknown shorthand")
	}
	if msg := err.Error(); !strings.Contains(msg, "'Z'") || !strings.Contains(msg, "--top") {
		t.Errorf("shorthand error does not name it and list the flags:\n%s", msg)
	}
	if code := output.ExitCodeFor(err); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

// TestOtherFlagErrorsPointAtHelpWithoutTheFlagList: a known flag given a bad
// value is not a typo in the flag name, so the list would only be noise; the
// --help pointer still helps and the exit code stays 2.
func TestOtherFlagErrorsPointAtHelpWithoutTheFlagList(t *testing.T) {
	newWorkspace(t)
	err := executeRoot(t, "search", "foo", "--top", "many")
	if err == nil {
		t.Fatal("no error for a bad flag value")
	}
	msg := err.Error()
	if !strings.Contains(msg, "llm-semantic search --help") {
		t.Errorf("no --help pointer:\n%s", msg)
	}
	if strings.Contains(msg, "valid flags") {
		t.Errorf("a bad value listed the flags:\n%s", msg)
	}
	if code := output.ExitCodeFor(err); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

// TestUnknownSubcommandIsRejectedByName defends the same principle for
// commands. At the root the error already named the command but listed nothing
// to choose from. Under a command group it was worse: `memory bogus` printed
// the group's help and exited 0, reporting success for a typo.
func TestUnknownSubcommandIsRejectedByName(t *testing.T) {
	cases := map[string]struct {
		args  []string
		path  string
		valid string
	}{
		"root":       {[]string{"bogus"}, "llm-semantic", "search"},
		"memory":     {[]string{"memory", "bogus"}, "llm-semantic memory", "store"},
		"collection": {[]string{"collection", "bogus"}, "llm-semantic collection", "delete"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			newWorkspace(t)
			fe := newFakeEmbedder(t)
			useEmbedder(t, fe)

			err := executeRoot(t, tc.args...)
			if err == nil {
				t.Fatal("no error for an unknown subcommand")
			}
			msg := err.Error()
			for _, want := range []string{`"bogus"`, tc.valid, tc.path + " --help"} {
				if !strings.Contains(msg, want) {
					t.Errorf("message lacks %q:\n%s", want, msg)
				}
			}
			if code := output.ExitCodeFor(err); code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if help := axiHelpOf(err); len(help) == 0 {
				t.Error("error carries no --axi help")
			}
			if n := fe.callCount(); n != 0 {
				t.Errorf("embedder received %d calls", n)
			}
		})
	}
}

// TestCommandGroupWithNoSubcommandStillShowsHelp: rejecting `memory bogus`
// must not turn a bare `memory` into an error.
func TestCommandGroupWithNoSubcommandStillShowsHelp(t *testing.T) {
	for _, group := range []string{"memory", "collection"} {
		t.Run(group, func(t *testing.T) {
			newWorkspace(t)
			root := RootCmd()
			root.SetArgs([]string{group})
			var err error
			out := captureStdout(t, func() { err = root.Execute() })
			if err != nil {
				t.Fatalf("bare %s failed: %v", group, err)
			}
			if !strings.Contains(out, "Available Commands:") {
				t.Errorf("bare %s did not print its help:\n%s", group, out)
			}
		})
	}
}
