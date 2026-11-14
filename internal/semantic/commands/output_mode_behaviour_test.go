package commands

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/pkg/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// everyCommand walks the tree RootCmd builds, root included, so a command
// added later is covered by the tables below without anyone editing them.
func everyCommand(root *cobra.Command) []*cobra.Command {
	cmds := []*cobra.Command{root}
	for _, c := range root.Commands() {
		if c.Name() == "help" || c.Name() == "completion" {
			continue
		}
		cmds = append(cmds, everyCommand(c)...)
	}
	return cmds
}

// commandArgs is the argument list that reaches c from the root.
func commandArgs(c *cobra.Command) []string {
	path := strings.Fields(c.CommandPath())
	return path[1:]
}

// requiredFlagArgs supplies every flag c marks required, so the invocation
// reaches the format check instead of stopping at a missing flag.
func requiredFlagArgs(c *cobra.Command) []string {
	var args []string
	c.Flags().VisitAll(func(f *pflag.Flag) {
		if _, ok := f.Annotations[cobra.BashCompOneRequiredFlag]; ok {
			args = append(args, "--"+f.Name, "x")
		}
	})
	return args
}

// commandAt returns the command at path in a fresh tree.
func commandAt(t *testing.T, root *cobra.Command, path []string) *cobra.Command {
	t.Helper()
	c, _, err := root.Find(path)
	if err != nil || strings.Join(strings.Fields(c.CommandPath())[1:], " ") != strings.Join(path, " ") {
		t.Fatalf("command %q not found in a fresh tree: %v", path, err)
	}
	return c
}

// resetOutputGlobals restores the mode globals after a test, since the package
// shares them across every test that runs after it.
func resetOutputGlobals(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		GlobalAXIOutput = false
		GlobalJSONOutput = false
		GlobalMinOutput = false
		output.SetDefaultAXI(false)
	})
}

func TestEveryCommandResolvesTheOutputMode(t *testing.T) {
	// Defends epic 27 T1: TOON is the default with no flag, --axi is a no-op
	// alias for it, --json is the compatibility mode, --plain is today's text
	// and --min modifies TOON or JSON. A command that kept the old plain
	// default, or read --axi itself, would print a different format from its
	// siblings and exit 0 while an agent parsed the wrong one.
	modes := []struct {
		name               string
		flags              []string
		toon, jsonOut, min bool
	}{
		{"default", nil, true, false, false},
		{"--axi", []string{"--axi"}, true, false, false},
		{"--axi=false", []string{"--axi=false"}, true, false, false},
		{"--json", []string{"--json"}, false, true, false},
		{"--json=false", []string{"--json=false"}, true, false, false},
		{"--plain", []string{"--plain"}, false, false, false},
		{"--min", []string{"--min"}, true, false, true},
		{"--axi --min", []string{"--axi", "--min"}, true, false, true},
		{"--json --min", []string{"--json", "--min"}, false, true, true},
		{"--min --json", []string{"--min", "--json"}, false, true, true},
	}
	resetOutputGlobals(t)
	t.Chdir(t.TempDir())

	for _, c := range everyCommand(RootCmd()) {
		path := commandArgs(c)
		for _, m := range modes {
			t.Run(strings.Join(append(append([]string{"root"}, path...), m.name), " "), func(t *testing.T) {
				root := RootCmd()
				target := commandAt(t, root, path)
				// Positional arguments are not under test; the probe only
				// records the mode the hooks resolved.
				target.Args = cobra.ArbitraryArgs
				ran := false
				var toon, jsonOut, min, formatterAXI bool
				target.RunE = func(*cobra.Command, []string) error {
					ran = true
					toon, jsonOut, min = GlobalAXIOutput, GlobalJSONOutput, GlobalMinOutput
					formatterAXI = output.New(false, false, nil).AXI
					return nil
				}
				target.Run = nil
				args := append(append(append([]string{}, path...), requiredFlagArgs(target)...), m.flags...)
				root.SetArgs(args)
				var err error
				captureStdout(t, func() { err = root.Execute() })
				if err != nil {
					t.Fatalf("Execute(%q) = %v, want nil", args, err)
				}
				if !ran {
					t.Fatalf("Execute(%q) never reached the command", args)
				}
				if toon != m.toon || jsonOut != m.jsonOut || min != m.min {
					t.Errorf("Execute(%q): toon=%v json=%v min=%v, want toon=%v json=%v min=%v",
						args, toon, jsonOut, min, m.toon, m.jsonOut, m.min)
				}
				if formatterAXI != m.toon {
					t.Errorf("Execute(%q): error formatter AXI=%v, want %v", args, formatterAXI, m.toon)
				}
			})
		}
	}
}

// rejectedFormats are the contradictory combinations, each in both orders.
var rejectedFormats = [][]string{
	{"--plain", "--json"}, {"--json", "--plain"},
	{"--plain", "--axi"}, {"--axi", "--plain"},
	{"--json", "--axi"}, {"--axi", "--json"},
	{"--plain", "--min"}, {"--min", "--plain"},
	{"--plain", "--json", "--axi"}, {"--axi", "--json", "--plain"},
}

func TestEveryCommandRejectsContradictoryFormatFlags(t *testing.T) {
	// Defends epic 27 T1: a contradictory pair is a usage error (exit 2), not
	// whichever flag happened to be checked first. Each pair is tried in both
	// orders and must produce the same message, so the outcome can never
	// depend on argument order. The check must run before the command, and
	// before its positional-argument check, so it is reported even when no
	// query is given.
	resetOutputGlobals(t)
	t.Chdir(t.TempDir())

	for _, c := range everyCommand(RootCmd()) {
		path := commandArgs(c)
		messages := map[string]string{}
		for _, flags := range rejectedFormats {
			t.Run(strings.Join(append(append([]string{"root"}, path...), flags...), " "), func(t *testing.T) {
				root := RootCmd()
				target := commandAt(t, root, path)
				target.RunE = func(*cobra.Command, []string) error {
					t.Errorf("command ran despite %q", flags)
					return nil
				}
				target.Run = nil
				args := append(append(append([]string{}, path...), requiredFlagArgs(target)...), flags...)
				root.SetArgs(args)
				var err error
				captureStdout(t, func() { err = root.Execute() })
				if err == nil {
					t.Fatalf("Execute(%q) = nil, want a usage error", args)
				}
				if code := output.ExitCodeFor(err); code != 2 {
					t.Errorf("Execute(%q) exit code = %d, want 2: %v", args, code, err)
				}
				for _, f := range flags {
					if !strings.Contains(err.Error(), f) {
						t.Errorf("Execute(%q) error does not name %s: %v", args, f, err)
					}
				}
				if len(axiHelpOf(err)) == 0 {
					t.Errorf("Execute(%q) error carries no help pointer", args)
				}
				key := sortedKey(flags)
				if prev, ok := messages[key]; ok && prev != err.Error() {
					t.Errorf("message depends on argument order:\n  %q\n  %q", prev, err.Error())
				}
				messages[key] = err.Error()
			})
		}
	}
}

// sortedKey identifies a flag set independently of its order.
func sortedKey(flags []string) string {
	s := append([]string{}, flags...)
	sort.Strings(s)
	return strings.Join(s, " ")
}

func TestBareRunPrintsEachModesFormat(t *testing.T) {
	// Defends epic 27 T1 end to end on a command that needs no embedder: the
	// bare run over a directory with no index. The probe above shows the mode
	// is resolved; this shows each mode reaches stdout in its own format, and
	// that --axi is byte-for-byte the default rather than a second variant.
	resetOutputGlobals(t)
	t.Chdir(t.TempDir())

	run := func(args ...string) string {
		root := RootCmd()
		// Never nil: cobra reads os.Args when no arguments were set.
		root.SetArgs(append([]string{}, args...))
		var err error
		out := captureStdout(t, func() { err = root.Execute() })
		if err != nil {
			t.Fatalf("Execute(%q) = %v", args, err)
		}
		return out
	}

	toon := run()
	if !strings.Contains(toon, "status: not_indexed") || strings.Contains(toon, "{") {
		t.Errorf("default output is not TOON:\n%s", toon)
	}
	if axi := run("--axi"); axi != toon {
		t.Errorf("--axi differs from the default:\n--axi:\n%s\ndefault:\n%s", axi, toon)
	}

	var payload map[string]any
	if out := run("--json"); json.Unmarshal([]byte(out), &payload) != nil || payload["status"] != "not_indexed" {
		t.Errorf("--json output is not the JSON payload:\n%s", out)
	}

	plain := run("--plain")
	if !strings.Contains(plain, "Status:  not_indexed") || strings.Contains(plain, "status: not_indexed") {
		t.Errorf("--plain output is not today's text:\n%s", plain)
	}
}
