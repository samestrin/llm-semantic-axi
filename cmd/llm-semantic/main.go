package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/samestrin/llm-semantic-axi/v2/internal/semantic/commands"
	"github.com/samestrin/llm-semantic-axi/v2/pkg/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Both are stamped at build time with -X main.version / -X main.commit, so the
// binary reports the tag it was built from. They were hardcoded at "1.5.0"
// inside llm-tools, which had no semantic build targets at all.
var (
	version = "dev"
	commit  = "dev"
)

func main() {
	rootCmd := commands.RootCmd()

	// -v/-V/--version asks for the version when no subcommand is named, even
	// beside the persistent flags (`--axi -v`). The flag is deliberately not
	// registered on the root: on a subcommand -v is --verbose (`index -v`),
	// and a root shorthand would collide with that. The stamp prints verbatim
	// whatever the format flags say (owner decision 2026-11-27, EST-9193 Q5).
	if versionRequested(rootCmd, os.Args[1:]) {
		fmt.Println(version)
		return
	}

	if err := rootCmd.Execute(); err != nil {
		// The command printed its own failure report; a second document on
		// stdout would leave it unparseable.
		var reported *output.ReportedError
		if errors.As(err, &reported) {
			os.Exit(output.ExitCodeFor(err))
		}
		// A flag-parse, argument-count or format-conflict error never reaches
		// PersistentPreRunE, so the output mode was not published and a
		// subcommand's local --json and --min were not copied to the globals;
		// read them from the arguments instead. TOON is the default, and it
		// is also the answer for contradictory format flags, which name no
		// single format: the choice must not depend on which came first.
		args := os.Args[1:]
		plain, jsonOut, axi, min := argsRequest(args, "plain"), argsRequest(args, "json"), argsRequest(args, "axi"), argsRequest(args, "min")
		if commands.FormatFlagConflict(plain, jsonOut, axi, min) != nil {
			// The root's --json is bound during parsing, so clear it too.
			plain, jsonOut, commands.GlobalJSONOutput = false, false, false
		}
		if jsonOut {
			commands.GlobalJSONOutput = true
		}
		if min {
			commands.GlobalMinOutput = true
		}
		output.SetDefaultAXI(!commands.GlobalJSONOutput && !plain)
		f := output.New(commands.GlobalJSONOutput, commands.GlobalMinOutput, os.Stdout)
		os.Exit(f.PrintError(err))
	}
}

// versionRequested reports whether args ask for the version: a -v/-V/--version
// with only persistent flags beside it and no subcommand or positional argument.
// It parses against the root's own persistent flags, so a valued flag's value
// (`--config index`) is never mistaken for a subcommand and a persistent flag
// added later is accepted without editing this function. Any parse failure
// returns false: the real Execute() must report the malformed invocation, not
// this probe. The probe shares the root's flag variables, which is safe because
// Execute() re-parses the same arguments whenever the probe says no.
func versionRequested(root *cobra.Command, args []string) bool {
	probe := make([]string, len(args))
	for i, a := range args {
		if a == "-V" {
			// pflag allows one shorthand per flag; -V aliases --version here.
			a = "--version"
		}
		probe[i] = a
	}
	fs := pflag.NewFlagSet("version-probe", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.AddFlagSet(root.PersistentFlags())
	var requested bool
	fs.BoolVarP(&requested, "version", "v", false, "")
	if err := fs.Parse(probe); err != nil {
		return false
	}
	return requested && fs.NArg() == 0
}

// argsRequest reports whether the arguments ask for the boolean flag --name,
// ignoring anything after a "--" terminator and honouring --name=false.
func argsRequest(args []string, name string) bool {
	requested := false
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "--"+name {
			requested = true
		} else if v, ok := strings.CutPrefix(a, "--"+name+"="); ok {
			b, err := strconv.ParseBool(v)
			requested = err == nil && b
		}
	}
	return requested
}
