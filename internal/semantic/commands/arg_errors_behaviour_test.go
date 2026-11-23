package commands

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/v2/pkg/output"
	"github.com/spf13/cobra"
)

// assertUsageError checks the parts every translated invocation error shares:
// the message names what was wrong and carries none of cobra's or pflag's raw
// wording, it classifies as exit 2, and the --axi help shows the right form.
func assertUsageError(t *testing.T, err error, want []string, notWant []string, helpWant string) {
	t.Helper()
	if err == nil {
		t.Fatal("no error for a malformed invocation")
	}
	msg := err.Error()
	for _, w := range want {
		if !strings.Contains(msg, w) {
			t.Errorf("message lacks %q:\n%s", w, msg)
		}
	}
	for _, w := range notWant {
		if strings.Contains(msg, w) {
			t.Errorf("message still carries the raw %q:\n%s", w, msg)
		}
	}
	if code := output.ExitCodeFor(err); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	help := strings.Join(axiHelpOf(err), "\n")
	if !strings.Contains(help, helpWant) {
		t.Errorf("--axi help lacks %q:\n%s", helpWant, help)
	}
}

// TestMissingArgumentIsNamedWithTheRightForm defends axi.md review round 2
// item 1: `callers` with no symbol said "accepts 1 arg(s), received 0" and
// `search` with no query "requires at least 1 arg(s), only received 0". Neither
// named the argument or showed the form to retry with, and neither had help,
// so an agent spent a second call on --help to learn it needed a <symbol>.
func TestMissingArgumentIsNamedWithTheRightForm(t *testing.T) {
	cases := map[string]struct {
		args []string
		arg  string
		form string
	}{
		"callers":       {[]string{"callers"}, "<symbol>", "llm-semantic callers <symbol>"},
		"refs":          {[]string{"refs"}, "<symbol>", "llm-semantic refs <symbol>"},
		"search":        {[]string{"search"}, "<query>", "llm-semantic search <query>"},
		"code-search":   {[]string{"code-search"}, "<query>", "llm-semantic code-search <query>"},
		"multisearch":   {[]string{"multisearch"}, "<query1>", "llm-semantic multisearch <query1>"},
		"memory search": {[]string{"memory", "search"}, "<query>", "llm-semantic memory search <query>"},
		"memory delete": {[]string{"memory", "delete"}, "<id>", "llm-semantic memory delete <id>"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			newWorkspace(t)
			fe := newFakeEmbedder(t)
			useEmbedder(t, fe)
			err := executeRoot(t, tc.args...)
			assertUsageError(t, err, []string{"missing", tc.arg, tc.form}, []string{"arg(s)"}, tc.form)
			if n := fe.callCount(); n != 0 {
				t.Errorf("embedder received %d calls before the arguments were rejected", n)
			}
		})
	}
}

// TestTooManyArgumentsAreNamed: `callers a b` said "accepts 1 arg(s), received
// 2". The error must say which arguments were extra and show the form.
func TestTooManyArgumentsAreNamed(t *testing.T) {
	newWorkspace(t)
	err := executeRoot(t, "callers", "a", "b")
	assertUsageError(t, err, []string{"too many arguments", `"b"`, "llm-semantic callers <symbol>"}, []string{"arg(s)"}, "llm-semantic callers <symbol>")
}

// TestIndexStatusRejectsAnExtraArgument defends review round 2 item 2:
// `index-status extra` had no Args validator, so the stray argument was
// silently ignored and the command reported on the current directory as if
// asked. It must be refused as a malformed invocation that names the argument.
func TestIndexStatusRejectsAnExtraArgument(t *testing.T) {
	newWorkspace(t)
	err := executeRoot(t, "index-status", "extra")
	assertUsageError(t, err, []string{"takes no arguments", `"extra"`}, []string{"unknown command"}, "llm-semantic index-status")
}

// TestEveryLeafCommandValidatesItsArguments is the audit behind item 2: a leaf
// command with no Args validator accepts and ignores any positional argument.
// A new command must declare what it takes.
func TestEveryLeafCommandValidatesItsArguments(t *testing.T) {
	// The tree before RootCmd's wrappers, which install a check on every
	// command and would hide a missing one.
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		if !c.HasSubCommands() && c.Args == nil {
			t.Errorf("%s has no Args validator, so it silently ignores extra arguments", c.CommandPath())
		}
	}
	for _, c := range unwrappedCommands() {
		walk(c)
	}
}

// TestMissingRequiredFlagIsNamedWithTheRightForm defends item 1:
// `collection delete` said `required flag(s) "profile" not set` with no help.
// It must name the flag and show a form that includes it.
func TestMissingRequiredFlagIsNamedWithTheRightForm(t *testing.T) {
	cases := map[string]struct {
		args []string
		flag string
		help string
	}{
		"collection delete": {[]string{"collection", "delete"}, "--profile", "llm-semantic collection delete --profile"},
		"memory store":      {[]string{"memory", "store", "-q", "why?"}, "--answer", "llm-semantic memory store"},
		"memory promote":    {[]string{"memory", "promote", "mem-1"}, "--target", "llm-semantic memory promote <id> --target"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			newWorkspace(t)
			err := executeRoot(t, tc.args...)
			assertUsageError(t, err, []string{"missing required flag", tc.flag}, []string{"required flag(s)"}, tc.help)
			if _, statErr := os.Stat(".index"); !os.IsNotExist(statErr) {
				t.Errorf("an index directory was created before the flag was rejected: %v", statErr)
			}
		})
	}
}

// TestCollectionDeleteWithAPositionalNamesItAndThePlaceForIt: the review's
// `collection delete x` — the domain goes in --profile, not as an argument.
func TestCollectionDeleteWithAPositionalNamesItAndThePlaceForIt(t *testing.T) {
	newWorkspace(t)
	err := executeRoot(t, "collection", "delete", "x")
	assertUsageError(t, err, []string{"takes no arguments", `"x"`, "--profile"}, []string{"unknown command", "required flag(s)"}, "llm-semantic collection delete --profile")
}

// TestBadFlagValueNamesTheExpectedType defends item 1: `memory stats --prune
// --older-than 1d` leaked `strconv.ParseInt: parsing "1d": invalid syntax`.
// It must name the value, the flag and what the flag takes, and show a form.
func TestBadFlagValueNamesTheExpectedType(t *testing.T) {
	cases := map[string]struct {
		args []string
		want []string
		help string
	}{
		"int":   {[]string{"memory", "stats", "--prune", "--older-than", "1d", "--yes"}, []string{`"1d"`, "--older-than", "whole number"}, "llm-semantic memory stats --prune --older-than 30"},
		"short": {[]string{"search", "foo", "-n", "many"}, []string{`"many"`, "--top", "whole number"}, "llm-semantic search"},
		"float": {[]string{"search", "foo", "--threshold", "high"}, []string{`"high"`, "--threshold", "number"}, "llm-semantic search"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			newWorkspace(t)
			err := executeRoot(t, tc.args...)
			assertUsageError(t, err, tc.want, []string{"strconv.", "invalid syntax"}, tc.help)
		})
	}
}

// TestFlagWithoutValueNamesTheFlag: `search foo --top` said "flag needs an
// argument: --top"; it must say what value the flag takes.
func TestFlagWithoutValueNamesTheFlag(t *testing.T) {
	newWorkspace(t)
	err := executeRoot(t, "search", "foo", "--top")
	assertUsageError(t, err, []string{"--top needs a value", "whole number"}, nil, "llm-semantic search")
}

// TestCommandsShowExamplesInHelp defends item 4 (AXI principle 10): these
// commands had no Examples section in --help, which AXI asks to carry 2-3.
func TestCommandsShowExamplesInHelp(t *testing.T) {
	for _, name := range []string{"search", "code-search", "docs-search", "multisearch", "index", "index-update", "index-status", "setup"} {
		t.Run(name, func(t *testing.T) {
			root := RootCmd()
			c, _, err := root.Find([]string{name})
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, line := range strings.Split(c.Example, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "llm-semantic "+name) {
					n++
				}
			}
			if n < 2 || n > 3 {
				t.Errorf("%s --help has %d examples, want 2-3:\n%s", name, n, c.Example)
			}
		})
	}
}

// TestFlagValueErrorsNameEveryFlagTypeInUse defends the review of EST-8249:
// valueKind's wording was only exercised for int and float flags. Each flag
// type a command declares must get its own words, end to end.
func TestFlagValueErrorsNameEveryFlagTypeInUse(t *testing.T) {
	cases := map[string]struct {
		args []string
		want []string
	}{
		"bool":         {[]string{"search", "foo", "--hybrid=nope"}, []string{`"nope"`, "--hybrid", "true or false"}},
		"string slice": {[]string{"search", "foo", "--profiles"}, []string{"--profiles needs a value", "comma-separated or repeated"}},
		"string array": {[]string{"setup", "--target"}, []string{"--target needs a value", "comma-separated or repeated"}},
		"string":       {[]string{"search", "foo", "--path"}, []string{"--path needs a value", "a string value"}},
		"shorthand":    {[]string{"search", "foo", "-n"}, []string{"--top needs a value", "whole number"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			newWorkspace(t)
			err := executeRoot(t, tc.args...)
			assertUsageError(t, err, tc.want, []string{"strconv.", "flag needs an argument"}, "Usage: `llm-semantic")
		})
	}
}

// TestFlagValueErrorLeavesUnrecognisedErrorsToTheCaller: when the error names
// a flag the command does not have, or a value that will not unquote, there
// is nothing to translate; flagError's generic path must handle it instead.
func TestFlagValueErrorLeavesUnrecognisedErrorsToTheCaller(t *testing.T) {
	cmd, _, err := RootCmd().Find([]string{"search"})
	if err != nil {
		t.Fatal(err)
	}
	for name, e := range map[string]error{
		"unknown flag in value error": errors.New(`invalid argument "x" for "--nosuch" flag: bad`),
		"value that will not unquote": errors.New(`invalid argument "\q" for "--top" flag: bad`),
		"unknown flag needs value":    errors.New("flag needs an argument: --nosuch"),
		"unrelated error":             errors.New("something else"),
	} {
		if got := flagValueError(cmd, e); got != nil {
			t.Errorf("%s: translated to %v, want nil", name, got)
		}
	}
	// And the generic path still classifies such an error as exit 2.
	if code := output.ExitCodeFor(flagError(cmd, errors.New(`invalid argument "x" for "--nosuch" flag: bad`))); code != 2 {
		t.Errorf("generic flag error exit = %d, want 2", code)
	}
}
