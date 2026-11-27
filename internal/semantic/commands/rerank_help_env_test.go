package commands

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Epic 39 T1, debt row 1308f565d5561cac: the alias commands' --rerank help
// named RERANKER_API_URL, a variable nothing reads. A user who followed that
// help set the wrong variable, got no reranking and no message. Every
// --rerank help string must name the variable the code actually reads.

// TestEveryRerankHelpNamesTheRealEnvVar walks every command and flag the CLI
// exposes: each --rerank usage string must carry the full variable name, and
// no help text may name the short, nonexistent RERANKER_API_URL.
func TestEveryRerankHelpNamesTheRealEnvVar(t *testing.T) {
	want := rerankerAPIURLEnvVar
	rerankFlags := 0
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		check := func(where, text string) {
			if strings.Contains(text, "RERANKER_API_URL") && !strings.Contains(text, want) {
				t.Errorf("%s names an env var that does not exist (want %s): %q", where, want, text)
			}
		}
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Name == "rerank" {
				rerankFlags++
				if !strings.Contains(f.Usage, want) {
					t.Errorf("%s --rerank help does not name %s: %q", cmd.CommandPath(), want, f.Usage)
				}
			}
			check(cmd.CommandPath()+" --"+f.Name, f.Usage)
		})
		check(cmd.CommandPath()+" Short", cmd.Short)
		check(cmd.CommandPath()+" Long", cmd.Long)
		check(cmd.CommandPath()+" Example", cmd.Example)
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(RootCmd())
	// A walk that found no --rerank flags checked nothing; that must fail too.
	if rerankFlags == 0 {
		t.Fatal("no --rerank flags found under RootCmd(); the walk checked nothing")
	}
}

// TestRerankerEnvNameMatchesWhatTheCodeReads pins the shared constant to the
// real variable name and pins getDefaultRerankerAPIURL to the constant, so
// help built from the constant is only green while the constant is the name
// the code actually reads.
func TestRerankerEnvNameMatchesWhatTheCodeReads(t *testing.T) {
	if rerankerAPIURLEnvVar != "LLM_SEMANTIC_RERANKER_API_URL" {
		t.Fatalf("rerankerAPIURLEnvVar = %q, want LLM_SEMANTIC_RERANKER_API_URL", rerankerAPIURLEnvVar)
	}
	want := rerankerAPIURLEnvVar
	t.Setenv(want, "http://reranker.example.invalid")
	if got := getDefaultRerankerAPIURL(); got != "http://reranker.example.invalid" {
		t.Fatalf("getDefaultRerankerAPIURL() = %q with %s set; the help names a variable the code does not read", got, want)
	}
}
