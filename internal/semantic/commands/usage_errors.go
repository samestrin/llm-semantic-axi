package commands

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/samestrin/llm-semantic-axi/v2/pkg/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// This file turns cobra's and pflag's invocation errors into messages that
// name what was wrong and show the form to retry with. Their own wording
// ("accepts 1 arg(s), received 0", `required flag(s) "profile" not set`,
// `strconv.ParseInt: parsing "1d": invalid syntax`) says something failed but
// not which argument, or what a valid value looks like, so an agent had to
// spend a second call on --help. Each error is an output.UsageError, so it
// exits 2 whatever its wording.

// usageError is a malformed invocation of cmd: msg, then the form and a
// pointer to --help, with the same in the --axi help. extra help lines, such
// as an example, come before the --help pointer.
func usageError(cmd *cobra.Command, msg string, extra ...string) error {
	form := usageForm(cmd)
	err := fmt.Errorf("%s; usage: `%s`; run `%s --help` for usage", msg, form, cmd.CommandPath())
	help := append([]string{fmt.Sprintf("Usage: `%s`", form)}, extra...)
	return withHelp(&output.UsageError{Err: err}, append(help, helpPointer(cmd))...)
}

// usageForm is cmd's invocation with its positional arguments, as its Use line
// names them, and its required flags spelled out.
func usageForm(cmd *cobra.Command) string {
	parts := []string{cmd.CommandPath()}
	if _, args, ok := strings.Cut(cmd.Use, " "); ok {
		parts = append(parts, args)
	}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if isRequiredFlag(f) {
			parts = append(parts, fmt.Sprintf("--%s <%s>", f.Name, f.Name))
		}
	})
	return strings.Join(parts, " ")
}

func isRequiredFlag(f *pflag.Flag) bool {
	v := f.Annotations[cobra.BashCompOneRequiredFlag]
	return len(v) == 1 && v[0] == "true"
}

// exampleFor returns the first example invocation of cmd that contains want,
// from its Example section or, for commands that keep it there, its Long text.
func exampleFor(cmd *cobra.Command, want string) string {
	for _, src := range []string{cmd.Example, cmd.Long} {
		for _, line := range strings.Split(src, "\n") {
			line = strings.TrimSpace(line)
			if i := strings.Index(line, "  #"); i >= 0 {
				line = strings.TrimSpace(line[:i])
			}
			if strings.HasPrefix(line, cmd.CommandPath()+" ") && strings.Contains(line, want) {
				return line
			}
		}
	}
	return ""
}

// exampleHelp is an "Example:" help line for cmd containing want, or none.
func exampleHelp(cmd *cobra.Command, want string) []string {
	if ex := exampleFor(cmd, want); ex != "" {
		return []string{fmt.Sprintf("Example: `%s`", ex)}
	}
	return nil
}

// positionalNames are the argument names in cmd's Use line, such as <symbol>
// or [path]; only the <...> ones are required.
func positionalNames(cmd *cobra.Command) (required, all []string) {
	_, args, _ := strings.Cut(cmd.Use, " ")
	for _, a := range strings.Fields(args) {
		if a == "..." {
			continue
		}
		all = append(all, a)
		if strings.HasPrefix(a, "<") {
			required = append(required, a)
		}
	}
	return required, all
}

// explainArgErrors replaces cobra's positional-argument errors on every leaf
// command with ones that name the argument. Command groups are left alone:
// rejectUnknownCommands already names an unknown subcommand.
func explainArgErrors(cmd *cobra.Command) {
	for _, c := range cmd.Commands() {
		explainArgErrors(c)
	}
	if cmd.HasSubCommands() || cmd.Args == nil {
		return
	}
	check := cmd.Args
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if check(cmd, args) == nil {
			return nil
		}
		return argCountError(cmd, args)
	}
}

// argCountError says which arguments are missing or extra.
func argCountError(cmd *cobra.Command, args []string) error {
	required, all := positionalNames(cmd)
	ex := exampleHelp(cmd, "")
	if len(args) < len(required) {
		return usageError(cmd, fmt.Sprintf("missing argument %s for %s", strings.Join(required[len(args):], " "), cmd.CommandPath()), ex...)
	}
	quoted := make([]string, 0, len(args))
	for _, a := range args[min(len(all), len(args)):] {
		quoted = append(quoted, strconv.Quote(a))
	}
	if len(all) == 0 {
		msg := fmt.Sprintf("%s takes no arguments; received %s", cmd.CommandPath(), strings.Join(quoted, ", "))
		var flags []string
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if isRequiredFlag(f) {
				flags = append(flags, "--"+f.Name)
			}
		})
		if len(flags) > 0 {
			msg += fmt.Sprintf(" (pass the value with %s)", strings.Join(flags, ", "))
		}
		return usageError(cmd, msg, ex...)
	}
	return usageError(cmd, fmt.Sprintf("too many arguments for %s: it takes %s; received %d, extra %s",
		cmd.CommandPath(), strings.Join(all, " "), len(args), strings.Join(quoted, ", ")), ex...)
}

// requiredFlagsError names the required flags cmd was not given. cobra checks
// them only after PersistentPreRunE, with no form or help; this runs first.
func requiredFlagsError(cmd *cobra.Command) error {
	var missing []string
	var first *pflag.Flag
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if isRequiredFlag(f) && !f.Changed {
			missing = append(missing, "--"+f.Name)
			if first == nil {
				first = f
			}
		}
	})
	if len(missing) == 0 {
		return nil
	}
	msg := fmt.Sprintf("missing required flag %s for %s (%s: %s)", strings.Join(missing, ", "), cmd.CommandPath(), missing[0], first.Usage)
	return usageError(cmd, msg, exampleHelp(cmd, missing[0])...)
}

var (
	invalidValueRE = regexp.MustCompile(`^invalid argument ("(?:[^"\\]|\\.)*") for "(?:-., )?--([^"]+)" flag: `)
	needsValueRE   = regexp.MustCompile(`^flag needs an argument: (?:--(\S+)|'(.)' in -\S+)$`)
)

// flagValueError translates pflag's error for a bad or missing flag value, or
// returns nil when err is neither.
func flagValueError(cmd *cobra.Command, err error) error {
	msg := err.Error()
	if m := invalidValueRE.FindStringSubmatch(msg); m != nil {
		f := cmd.Flags().Lookup(m[2])
		value, uerr := strconv.Unquote(m[1])
		if f == nil || uerr != nil {
			return nil
		}
		return usageError(cmd, fmt.Sprintf("invalid value %q for --%s: it takes %s (%s)", value, f.Name, valueKind(f), f.Usage),
			exampleHelp(cmd, "--"+f.Name)...)
	}
	if m := needsValueRE.FindStringSubmatch(msg); m != nil {
		var f *pflag.Flag
		if m[1] != "" {
			f = cmd.Flags().Lookup(m[1])
		} else {
			f = cmd.Flags().ShorthandLookup(m[2])
		}
		if f == nil {
			return nil
		}
		return usageError(cmd, fmt.Sprintf("--%s needs a value: %s (%s)", f.Name, valueKind(f), f.Usage),
			exampleHelp(cmd, "--"+f.Name)...)
	}
	return nil
}

// valueKind says in words what a flag's value must be. It covers the flag
// types the commands declare; a new type falls to the generic wording.
func valueKind(f *pflag.Flag) string {
	switch t := f.Value.Type(); t {
	case "int":
		return "a whole number, such as 30"
	case "float64":
		return "a number, such as 0.5"
	case "bool":
		return "true or false"
	case "stringSlice", "stringArray":
		return "a value (comma-separated or repeated)"
	default:
		return "a " + t + " value"
	}
}
