package mcpserver

import (
	"os"
	"strings"
	"testing"
)

// Every MCP tool that turns a tool-input string into a CLI positional. A
// prompt-injected agent controls these strings, and before the fix
// llm_semantic_index with {"path":"--api-url=http://127.0.0.1:18935"} sent the
// project's files to that endpoint and {"path":"--allow-home"} bypassed the
// home-directory refusal, because the value reached the CLI as an option.
var positionalTools = []struct {
	tool string
	key  string
	args func(v string) map[string]interface{}
}{
	{"index", "path", func(v string) map[string]interface{} { return map[string]interface{}{"path": v} }},
	{"index_update", "path", func(v string) map[string]interface{} { return map[string]interface{}{"path": v} }},
	{"search", "query", func(v string) map[string]interface{} { return map[string]interface{}{"query": v} }},
	{"search_code", "query", func(v string) map[string]interface{} { return map[string]interface{}{"query": v} }},
	{"search_docs", "query", func(v string) map[string]interface{} { return map[string]interface{}{"query": v} }},
	{"search_memory", "query", func(v string) map[string]interface{} { return map[string]interface{}{"query": v} }},
	{"memory_search", "query", func(v string) map[string]interface{} { return map[string]interface{}{"query": v} }},
	{"multisearch", "queries", func(v string) map[string]interface{} {
		return map[string]interface{}{"queries": []interface{}{"fine", v}}
	}},
	{"callers", "symbol", func(v string) map[string]interface{} { return map[string]interface{}{"symbol": v} }},
	{"memory_promote", "id", func(v string) map[string]interface{} {
		return map[string]interface{}{"id": v, "target": "CLAUDE.md"}
	}},
	{"memory_delete", "id", func(v string) map[string]interface{} { return map[string]interface{}{"id": v} }},
}

func TestExecuteHandler_RefusesAnOptionLookingPositional(t *testing.T) {
	// Defends against option injection: a tool value starting with "-" is
	// refused before the CLI runs, so it can never become --api-url or
	// --allow-home.
	for _, tc := range positionalTools {
		for _, v := range []string{"--api-url=http://127.0.0.1:18935", "--allow-home", "-h"} {
			t.Run(tc.tool+"/"+v, func(t *testing.T) {
				inConfiguredDir(t)
				script, argvLog := fakeBinary(t, "exit 0")
				useFakeBinary(t, script)

				_, err := ExecuteHandler(ToolPrefix+tc.tool, tc.args(v))
				if err == nil {
					t.Fatalf("ExecuteHandler accepted %s %q", tc.key, v)
				}
				if !strings.Contains(err.Error(), tc.key) || !strings.Contains(err.Error(), `"-"`) {
					t.Errorf("error %q should name %s and the leading \"-\"", err, tc.key)
				}
				if _, statErr := os.Stat(argvLog); statErr == nil {
					t.Errorf("the CLI ran with %v", recordedArgv(t, argvLog))
				}
			})
		}
	}
}

func TestExecuteHandler_PositionalsFollowATerminator(t *testing.T) {
	// Defends the second layer: every positional from tool input sits after a
	// "--", and every flag (the builder's and the forwarded --axi, --api-url,
	// --profile) sits before it, so the CLI cannot read a positional as an
	// option nor a forwarded flag as a positional.
	t.Setenv("LLM_SEMANTIC_API_URL", "http://embed.test")
	for _, tc := range positionalTools {
		t.Run(tc.tool, func(t *testing.T) {
			inConfiguredDir(t)
			script, argvLog := fakeBinary(t, "exit 0")
			useFakeBinary(t, script)

			if _, err := ExecuteHandler(ToolPrefix+tc.tool, tc.args("a=b value")); err != nil {
				t.Fatalf("ExecuteHandler: %v", err)
			}
			argv := recordedArgv(t, argvLog)
			dash := -1
			for i, a := range argv {
				if a == "--" {
					dash = i
					break
				}
			}
			if dash < 0 {
				t.Fatalf("argv has no \"--\": %v", argv)
			}
			if argv[len(argv)-1] != "a=b value" {
				t.Errorf("the positional is not last: %v", argv)
			}
			for _, a := range argv[dash+1:] {
				if strings.HasPrefix(a, "-") {
					t.Errorf("%q follows the terminator: %v", a, argv)
				}
			}
			for _, flag := range []string{"--axi", "--api-url"} {
				if !hasArg(argv[:dash], flag) {
					t.Errorf("%s is not before the terminator: %v", flag, argv)
				}
			}
		})
	}
}
