package commands

import (
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestSearchJoinsUnquotedWordsWithSingleSpaces defends against the query
// reaching the embedder as "a b c " (trailing space) when words are passed
// unquoted, and against `search` and its profile alias disagreeing on the text.
func TestSearchJoinsUnquotedWordsWithSingleSpaces(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  func() *cobra.Command
	}{
		{"search", searchCmd},
		{"alias", func() *cobra.Command { return searchAliasCmd("code-search", "code") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			indexedWorkspace(t)
			fe := newFakeEmbedder(t)
			useEmbedder(t, fe)
			cmd := tc.cmd()
			cmd.SetArgs([]string{"a", "b", "c"})
			cmd.SetOut(&strings.Builder{})
			cmd.SetErr(&strings.Builder{})
			captureStdout(t, func() {
				if err := cmd.Execute(); err != nil {
					t.Fatalf("execute: %v", err)
				}
			})
			if !slices.Contains(fe.embeddedTexts(), "a b c") {
				t.Errorf("embedded texts = %q, want one equal to %q", fe.embeddedTexts(), "a b c")
			}
		})
	}
}
