package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/samestrin/llm-semantic-axi/pkg/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// GlobalAXIOutput is true when this invocation prints TOON, which it does
// unless --json or --plain asks for something else. It is resolved, not bound
// to a flag: PersistentPreRunE sets it from the parsed flags on every run, and
// RootCmd() clears it, so a fresh command tree never inherits an earlier
// invocation's mode.
var GlobalAXIOutput bool

// axiAlias is --axi. TOON is already the default, so it changes nothing; it is
// still accepted so that scripts and agents written against v1 keep working.
var axiAlias bool

// plainOutput is --plain: the human-readable text v1 printed by default.
var plainOutput bool

// flagOn reports whether cmd's flag name is set to true. Looked up on cmd, not
// read from the bound variable, because several subcommands declare their own
// local --json and --min, which shadow the root's.
func flagOn(cmd *cobra.Command, name string) bool {
	f := cmd.Flags().Lookup(name)
	return f != nil && f.Value.String() == "true"
}

// resolveOutputMode publishes the output mode the parsed flags ask for.
func resolveOutputMode(cmd *cobra.Command) {
	GlobalJSONOutput = flagOn(cmd, "json")
	GlobalMinOutput = flagOn(cmd, "min")
	GlobalAXIOutput = !GlobalJSONOutput && !flagOn(cmd, "plain")
	output.SetDefaultAXI(GlobalAXIOutput)
}

// FormatFlagConflict returns the output flags that cannot be used together,
// always in the same order, or nil when the combination is valid. --plain,
// --json and --axi each pick a format, so any two contradict; --min shapes a
// TOON or JSON payload and has nothing to shape under --plain.
//
// main() calls it too, to pick the error format for an invocation that failed
// before the flags were resolved, so the two can never disagree.
func FormatFlagConflict(plain, jsonOut, axi, min bool) []string {
	var flags []string
	for _, f := range []struct {
		on   bool
		name string
	}{{plain, "--plain"}, {jsonOut, "--json"}, {axi, "--axi"}} {
		if f.on {
			flags = append(flags, f.name)
		}
	}
	if plain && min {
		flags = append(flags, "--min")
	}
	if len(flags) < 2 {
		return nil
	}
	return flags
}

// formatConflictError rejects a contradictory set of output flags. Its wording
// is matched by output.ExitCodeFor, which classifies it as exit 2.
func formatConflictError(cmd *cobra.Command, flags []string) error {
	list := strings.Join(flags[:len(flags)-1], ", ") + " and " + flags[len(flags)-1]
	return withHelp(fmt.Errorf("output flags %s cannot be combined: TOON is the default, --json and --plain each replace it, and --min applies only to TOON or JSON; run `%s --help` for usage",
		list, cmd.CommandPath()), helpPointer(cmd))
}

// rejectContradictoryFormats makes every command refuse a contradictory set of
// output flags before anything else looks at the invocation.
//
// It wraps each command's argument check rather than living in a hook, because
// cobra checks arguments before PersistentPreRunE: `search --plain --json` with
// no query would otherwise be reported as a missing query, and the format
// conflict only once the query was supplied. Must run after
// rejectUnknownCommands, which replaces the argument check of command groups.
func rejectContradictoryFormats(cmd *cobra.Command) {
	for _, c := range cmd.Commands() {
		rejectContradictoryFormats(c)
	}
	check := cmd.Args
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if flags := FormatFlagConflict(flagOn(cmd, "plain"), flagOn(cmd, "json"), flagOn(cmd, "axi"), flagOn(cmd, "min")); flags != nil {
			return formatConflictError(cmd, flags)
		}
		if check != nil {
			return check(cmd, args)
		}
		return nil
	}
}

// emitJSON writes v as JSON, or as TOON when TOON is the resolved mode.
//
// This package has no output choke point — 23 sites built their own encoder
// across 8 files — so this is it. The signature carries both variations those
// sites actually used, because collapsing either would change output:
//
//   - a WRITER, because refs.go's writeCallersJSON/writeRefsJSON take an
//     io.Writer parameter rather than writing to os.Stdout;
//   - an INDENT choice, because index_status.go's "index not found" and
//     "embedder offline" branches emit COMPACT json (they build the encoder
//     inline with no SetIndent, unlike the other 21). Routing them through an
//     indenting helper would silently reformat their --json output.
func emitJSON(w io.Writer, v any, indent bool) error {
	if GlobalAXIOutput {
		return output.EncodeAXI(w, v)
	}
	enc := json.NewEncoder(w)
	if indent {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(v)
}

// emitStdout is the shape 21 of the 23 sites use: indented JSON on stdout.
func emitStdout(v any) error {
	return emitJSON(os.Stdout, v, true)
}

// helpError carries --axi next-step suggestions alongside an error, without
// changing its message, so plain and --json error output stay as they were.
type helpError struct {
	err  error
	help []string
}

func (e *helpError) Error() string     { return e.err.Error() }
func (e *helpError) Unwrap() error     { return e.err }
func (e *helpError) AXIHelp() []string { return e.help }

// withHelp attaches --axi help to err.
func withHelp(err error, help ...string) error {
	return &helpError{err: err, help: help}
}

// indexMissingHelp is the next step when no index exists.
var indexMissingHelp = []string{
	"Run `llm-semantic index <path>` to build the index, then repeat this command",
	"Run `llm-semantic index-status` to check which index this directory resolves to",
}

// helpPointer is the --axi next step for a malformed invocation of cmd.
func helpPointer(cmd *cobra.Command) string {
	return fmt.Sprintf("Run `%s --help` to see its commands and flags", cmd.CommandPath())
}

// flagError rejects a malformed flag with what the caller needs to retry: for
// an unknown flag, every flag cmd accepts, and for any flag error a pointer to
// --help. Without the list an agent learns only that its flag was wrong, and
// spends a second call on --help to find the right one.
//
// It wraps rather than replaces cobra's error, so the message still starts with
// cobra's wording and output.ExitCodeFor still classifies it as exit 2. It runs
// during flag parsing, before any hook, embedder or storage is reached.
func flagError(cmd *cobra.Command, err error) error {
	msg := err.Error()
	if strings.HasPrefix(msg, "unknown flag: ") || strings.HasPrefix(msg, "unknown shorthand flag: ") {
		var flags []string
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if !f.Hidden {
				flags = append(flags, "--"+f.Name)
			}
		})
		err = fmt.Errorf("%w; valid flags for %s: %s", err, cmd.CommandPath(), strings.Join(flags, ", "))
	}
	return withHelp(fmt.Errorf("%w; run `%s --help` for usage", err, cmd.CommandPath()), helpPointer(cmd))
}

// unknownCommandError names an argument that is not one of cmd's subcommands
// and lists the ones that are. Its leading wording is cobra's own, which
// output.ExitCodeFor classifies as exit 2.
func unknownCommandError(cmd *cobra.Command, name string) error {
	var names []string
	for _, c := range cmd.Commands() {
		if c.IsAvailableCommand() {
			names = append(names, c.Name())
		}
	}
	msg := fmt.Sprintf("unknown command %q for %q", name, cmd.CommandPath())
	// cobra sets its default distance only inside its own lookup, which this
	// path replaces; at zero, SuggestionsFor matches nothing but prefixes.
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = 2
	}
	if s := cmd.SuggestionsFor(name); len(s) > 0 {
		msg += fmt.Sprintf(" (did you mean %s?)", strings.Join(s, " or "))
	}
	msg += fmt.Sprintf("; valid commands: %s; run `%s --help` for usage", strings.Join(names, ", "), cmd.CommandPath())
	return withHelp(errors.New(msg), helpPointer(cmd))
}

// rejectUnknownCommands makes every command that has subcommands fail on an
// argument that names none of them.
//
// cobra checks this only at the root, and only when the root sets no Args. A
// command group with no Run of its own (`memory`, `collection`) printed its
// help and exited 0 for `memory bogus`: success reported for a typo. A group
// gains a Run that shows its help, so that its arguments are validated at all.
func rejectUnknownCommands(cmd *cobra.Command) {
	for _, c := range cmd.Commands() {
		rejectUnknownCommands(c)
	}
	if !cmd.HasSubCommands() {
		return
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return unknownCommandError(cmd, args[0])
		}
		return nil
	}
	if !cmd.Runnable() {
		cmd.RunE = func(cmd *cobra.Command, args []string) error { return cmd.Help() }
	}
}
