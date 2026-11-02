package commands

import (
	"encoding/json"
	"io"
	"os"

	"github.com/samestrin/llm-semantic-axi/pkg/output"
)

// GlobalAXIOutput selects TOON output.
//
// It is registered as a persistent flag only AFTER every emit site routes
// through emitJSON. A persistent flag that some commands ignore is worse than
// no flag: every command accepts it, so one that ignored it would print its
// ordinary output and exit 0 while a consumer parsed TOON — a failure with no
// error and no signal.
//
// Unlike llm-support, this package needs no reset helper. It builds a fresh
// command tree per invocation via RootCmd(), and BoolVar writes the default at
// registration, so the bound variable cannot carry a previous run's value.
// Verified rather than assumed by analogy: a probe confirmed GlobalJSONOutput
// and GlobalMinOutput both return to false after a fresh RootCmd().
var GlobalAXIOutput bool

// emitJSON writes v as JSON, or as TOON when --axi is set.
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
