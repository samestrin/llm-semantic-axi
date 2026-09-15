package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ExecuteHandler is the only code path the MCP server actually runs: it maps a
// tool name to a CLI invocation, shells out, and turns the subprocess result
// into an MCP response. These tests point BinaryPath at a recording script in a
// temp dir so the full path is exercised without the real binary.
//
// No t.Parallel in this package — BinaryPath and CommandTimeout are package
// level and every case here swaps one of them.

// fakeBinary writes an executable shell script that records the argv it was
// called with (one argument per line) and then runs body. It returns the path
// of the script and the path of the recording file.
func fakeBinary(t *testing.T, body string) (script, argvLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake binary is a POSIX shell script")
	}
	dir := t.TempDir()
	script = filepath.Join(dir, "llm-semantic")
	argvLog = filepath.Join(dir, "argv.txt")

	content := "#!/bin/sh\n" +
		": > " + argvLog + "\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> " + argvLog + "; done\n" +
		body + "\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return script, argvLog
}

// useFakeBinary installs the script as the binary ExecuteHandler shells out to
// and restores the real value afterwards.
func useFakeBinary(t *testing.T, script string) {
	t.Helper()
	original := BinaryPath
	BinaryPath = script
	t.Cleanup(func() { BinaryPath = original })
}

// recordedArgv reads back the argv the fake binary was invoked with.
func recordedArgv(t *testing.T, argvLog string) []string {
	t.Helper()
	data, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// inConfiguredDir moves the test into a scratch working directory holding a
// config at the default discovery path, so a profiled command can resolve.
// Profile resolution runs before argument validation, so any tool with a
// registry profile needs this even when the call is expected to fail later.
func inConfiguredDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".planning", ".config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	cfg := "semantic:\n" +
		"  code_collection: cfg_code\n" +
		"  code_storage: sqlite\n" +
		"  docs_collection: cfg_docs\n" +
		"  docs_storage: sqlite\n" +
		"  memory_collection: cfg_memory\n" +
		"  memory_storage: sqlite\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Chdir(dir)
}

// hasFlag reports whether argv carries flag immediately followed by value.
func hasFlag(argv []string, flag, value string) bool {
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

func hasArg(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

func TestExecuteHandler_ReturnsTheSubprocessOutputVerbatim(t *testing.T) {
	// The MCP layer hands the CLI's stdout straight back to the model, so it
	// must not be reformatted, trimmed or re-encoded on the way out.
	const payload = `{"count":2,"rows":[{"f":"a.go","l":1}]}`
	script, _ := fakeBinary(t, "printf '%s' '"+payload+"'")
	useFakeBinary(t, script)

	got, err := ExecuteHandler(ToolPrefix+"index_status", map[string]interface{}{})
	if err != nil {
		t.Fatalf("ExecuteHandler: %v", err)
	}
	if got != payload {
		t.Errorf("ExecuteHandler = %q, want %q", got, payload)
	}
}

func TestExecuteHandler_StripsTheToolPrefixAndPassesTheSubcommand(t *testing.T) {
	// The MCP tool is llm_semantic_index_status; the CLI subcommand is
	// index-status. Getting this mapping wrong means every tool call fails with
	// "unknown command" at the far end.
	script, argvLog := fakeBinary(t, "exit 0")
	useFakeBinary(t, script)

	if _, err := ExecuteHandler(ToolPrefix+"index_status", map[string]interface{}{
		"storage": "sqlite",
	}); err != nil {
		t.Fatalf("ExecuteHandler: %v", err)
	}

	argv := recordedArgv(t, argvLog)
	if argv[0] != "index-status" {
		t.Errorf("subcommand = %q, want %q (argv: %v)", argv[0], "index-status", argv)
	}
	if !hasFlag(argv, "--storage", "sqlite") {
		t.Errorf("--storage was not forwarded: %v", argv)
	}
}

func TestExecuteHandler_AlwaysAppendsJSONAndMin(t *testing.T) {
	// The MCP server parses the CLI's output, so the machine-readable,
	// token-optimized mode is not optional. Both flags are added once and only
	// once, even when a builder already supplied them.
	script, argvLog := fakeBinary(t, "exit 0")
	useFakeBinary(t, script)

	if _, err := ExecuteHandler(ToolPrefix+"search", map[string]interface{}{
		"query": "auth",
	}); err != nil {
		t.Fatalf("ExecuteHandler: %v", err)
	}

	argv := recordedArgv(t, argvLog)
	for _, flag := range []string{"--json", "--min"} {
		n := 0
		for _, a := range argv {
			if a == flag {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s appears %d times, want exactly 1: %v", flag, n, argv)
		}
	}
}

func TestExecuteHandler_ForwardsTheEmbeddingEnvironmentAsFlags(t *testing.T) {
	// The subprocess inherits the env, but the flags are forwarded explicitly so
	// a value changed after the server started still reaches the CLI. Dropping
	// them sends every search to whatever endpoint the server booted with.
	script, argvLog := fakeBinary(t, "exit 0")
	useFakeBinary(t, script)
	t.Setenv("LLM_SEMANTIC_API_URL", "http://127.0.0.1:9999")
	t.Setenv("LLM_SEMANTIC_MODEL", "test-embed-model")

	if _, err := ExecuteHandler(ToolPrefix+"search", map[string]interface{}{
		"query": "auth",
	}); err != nil {
		t.Fatalf("ExecuteHandler: %v", err)
	}

	argv := recordedArgv(t, argvLog)
	if !hasFlag(argv, "--api-url", "http://127.0.0.1:9999") {
		t.Errorf("--api-url was not forwarded: %v", argv)
	}
	if !hasFlag(argv, "--model", "test-embed-model") {
		t.Errorf("--model was not forwarded: %v", argv)
	}
}

func TestExecuteHandler_OmitsTheEnvironmentFlagsWhenUnset(t *testing.T) {
	// An empty env var must not become `--model ""`, which the CLI would take as
	// an explicit request for a nameless model rather than as "use the default".
	script, argvLog := fakeBinary(t, "exit 0")
	useFakeBinary(t, script)
	t.Setenv("LLM_SEMANTIC_API_URL", "")
	t.Setenv("LLM_SEMANTIC_MODEL", "")

	if _, err := ExecuteHandler(ToolPrefix+"search", map[string]interface{}{
		"query": "auth",
	}); err != nil {
		t.Fatalf("ExecuteHandler: %v", err)
	}

	argv := recordedArgv(t, argvLog)
	if hasArg(argv, "--api-url") || hasArg(argv, "--model") {
		t.Errorf("an unset env var was forwarded as an empty flag: %v", argv)
	}
}

func TestExecuteHandler_InjectsTheRegistryProfileAndItsConfigPath(t *testing.T) {
	// search_memory has no profile argument of its own; the registry supplies
	// "memory" so the CLI resolves the memory collection rather than the code
	// one. The default config path rides along, because the CLI needs it to look
	// the profile up.
	script, argvLog := fakeBinary(t, "exit 0")
	useFakeBinary(t, script)

	inConfiguredDir(t)

	args := map[string]interface{}{"query": "what did I decide"}
	if _, err := ExecuteHandler(ToolPrefix+"search_memory", args); err != nil {
		t.Fatalf("ExecuteHandler: %v", err)
	}

	argv := recordedArgv(t, argvLog)
	if !hasFlag(argv, "--profile", "memory") {
		t.Errorf("the registry profile was not injected: %v", argv)
	}
	if !hasFlag(argv, "--config", defaultConfigPath) {
		t.Errorf("the default config path was not passed alongside the profile: %v", argv)
	}
	if args["profile"] != "memory" {
		t.Errorf(`args["profile"] = %v, want "memory"`, args["profile"])
	}
	// The injected profile is what the config lookup keys on, so the memory
	// collection — not the code one — reaches the CLI.
	if !hasFlag(argv, "--collection", "cfg_memory") {
		t.Errorf("the memory profile's collection was not resolved: %v", argv)
	}
}

func TestExecuteHandler_DoesNotOverrideAnExplicitProfile(t *testing.T) {
	// A caller that names a profile outranks the registry default. Otherwise
	// search_memory could never be pointed at a different collection.
	script, argvLog := fakeBinary(t, "exit 0")
	useFakeBinary(t, script)
	inConfiguredDir(t)

	if _, err := ExecuteHandler(ToolPrefix+"search_memory", map[string]interface{}{
		"query":   "x",
		"profile": "docs",
	}); err != nil {
		t.Fatalf("ExecuteHandler: %v", err)
	}

	argv := recordedArgv(t, argvLog)
	if !hasFlag(argv, "--profile", "docs") {
		t.Errorf("the explicit profile was overwritten: %v", argv)
	}
	if hasFlag(argv, "--profile", "memory") {
		t.Errorf("the registry profile was injected over an explicit one: %v", argv)
	}
	if !hasFlag(argv, "--collection", "cfg_docs") {
		t.Errorf("the docs collection was not resolved from the config: %v", argv)
	}
}

func TestExecuteHandler_PassesAnExplicitConfigPathInsteadOfTheDefault(t *testing.T) {
	// When the caller supplies a config, that file is what the CLI must read —
	// silently substituting .planning/.config/config.yaml would resolve the
	// profile against the wrong collections.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := "semantic:\n  code_collection: my_code\n  code_storage: qdrant\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	script, argvLog := fakeBinary(t, "exit 0")
	useFakeBinary(t, script)

	if _, err := ExecuteHandler(ToolPrefix+"search_code", map[string]interface{}{
		"query":  "parser",
		"config": cfgPath,
	}); err != nil {
		t.Fatalf("ExecuteHandler: %v", err)
	}

	argv := recordedArgv(t, argvLog)
	if !hasFlag(argv, "--config", cfgPath) {
		t.Errorf("the explicit config path was not forwarded: %v", argv)
	}
	if hasFlag(argv, "--config", defaultConfigPath) {
		t.Errorf("the default config path was used as well: %v", argv)
	}
	// The profile's collection/storage were resolved from that file and passed
	// through, which is the reason the config is loaded at all.
	if !hasFlag(argv, "--collection", "my_code") {
		t.Errorf("the profile collection was not resolved from the config: %v", argv)
	}
	if !hasFlag(argv, "--storage", "qdrant") {
		t.Errorf("the profile storage was not resolved from the config: %v", argv)
	}
}

func TestExecuteHandler_OmitsConfigAndProfileForAProfilelessCommand(t *testing.T) {
	// index_status carries no registry profile, so nothing should manufacture
	// one. Passing --config for a command that has no profile makes the CLI read
	// a file it does not need and fail when it is absent.
	script, argvLog := fakeBinary(t, "exit 0")
	useFakeBinary(t, script)
	t.Chdir(t.TempDir())

	if _, err := ExecuteHandler(ToolPrefix+"index_status", map[string]interface{}{}); err != nil {
		t.Fatalf("ExecuteHandler: %v", err)
	}

	argv := recordedArgv(t, argvLog)
	if hasArg(argv, "--config") || hasArg(argv, "--profile") {
		t.Errorf("config/profile were added to a profileless command: %v", argv)
	}
}

func TestExecuteHandler_ReportsAFailedSubprocessWithItsOutput(t *testing.T) {
	// A non-zero exit has to surface as an error, and the subprocess's own
	// output has to come with it — that text is usually the only diagnosis
	// available ("no index found", "connection refused").
	script, _ := fakeBinary(t, "printf 'no index at .index\\n' >&2; exit 3")
	useFakeBinary(t, script)

	out, err := ExecuteHandler(ToolPrefix+"index_status", map[string]interface{}{})
	if err == nil {
		t.Fatal("a failing subprocess was reported as success")
	}
	if out != "" {
		t.Errorf("output was returned alongside the error: %q", out)
	}
	msg := err.Error()
	if !strings.Contains(msg, "index_status") {
		t.Errorf("error does not name the command: %v", err)
	}
	if !strings.Contains(msg, "no index at .index") {
		t.Errorf("the subprocess output was dropped from the error: %v", err)
	}
}

func TestExecuteHandler_ReportsAFailureThatPrintedNothing(t *testing.T) {
	// A silent non-zero exit still has to be an error. The message has no output
	// to append, and must not end with a dangling "Output:" header.
	script, _ := fakeBinary(t, "exit 4")
	useFakeBinary(t, script)

	_, err := ExecuteHandler(ToolPrefix+"index_status", map[string]interface{}{})
	if err == nil {
		t.Fatal("a failing subprocess was reported as success")
	}
	if strings.Contains(err.Error(), "Output:") {
		t.Errorf("an empty output section was appended: %v", err)
	}
}

func TestExecuteHandler_ReportsAMissingBinary(t *testing.T) {
	// An install where llm-semantic is absent must fail per call with a readable
	// message rather than hanging or returning empty output that reads as "no
	// results".
	useFakeBinary(t, filepath.Join(t.TempDir(), "does-not-exist"))

	if _, err := ExecuteHandler(ToolPrefix+"index_status", map[string]interface{}{}); err == nil {
		t.Fatal("a missing binary was reported as success")
	}
}

func TestExecuteHandler_TimesOutARunawayCommand(t *testing.T) {
	// index runs can be slow, but they cannot be unbounded: an MCP client
	// blocked forever on one tool call is worse than a failed one. The timeout
	// message has to say so explicitly rather than surfacing "signal: killed".
	script, _ := fakeBinary(t, "sleep 30")
	useFakeBinary(t, script)

	originalTimeout := CommandTimeout
	CommandTimeout = 150 * time.Millisecond
	t.Cleanup(func() { CommandTimeout = originalTimeout })

	start := time.Now()
	_, err := ExecuteHandler(ToolPrefix+"index_status", map[string]interface{}{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a runaway command was reported as success")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error does not identify the timeout: %v", err)
	}
	if elapsed > 10*time.Second {
		t.Errorf("the timeout did not cut the command short: took %v", elapsed)
	}
}

func TestExecuteHandler_RejectsAnUnknownTool(t *testing.T) {
	// A tool name with no registry entry must be refused before anything is
	// executed. Shelling out with an unmapped name would hand arbitrary text to
	// the CLI as a subcommand.
	script, argvLog := fakeBinary(t, "exit 0")
	useFakeBinary(t, script)

	if _, err := ExecuteHandler(ToolPrefix+"not_a_real_tool", map[string]interface{}{}); err == nil {
		t.Fatal("an unknown tool was accepted")
	} else if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(argvLog); statErr == nil {
		t.Error("the binary was invoked for an unknown tool")
	}
}

func TestExecuteHandler_RejectsInvalidArgumentsBeforeExecuting(t *testing.T) {
	// Validation runs ahead of the subprocess, so a bad call costs nothing and
	// the message names the missing field instead of coming back as CLI usage
	// text the model has to interpret.
	for name, tc := range map[string]struct {
		tool string
		args map[string]interface{}
		want string
	}{
		"search without a query": {
			tool: "search", args: map[string]interface{}{}, want: "non-empty query",
		},
		"search with a blank query": {
			tool: "search", args: map[string]interface{}{"query": "   "}, want: "non-empty query",
		},
		"memory_search without a query": {
			tool: "memory_search", args: map[string]interface{}{}, want: "non-empty query",
		},
		"callers without a symbol": {
			tool: "callers", args: map[string]interface{}{"symbol": ""}, want: "non-empty symbol",
		},
		"multisearch with no queries": {
			tool: "multisearch", args: map[string]interface{}{}, want: "at least one query",
		},
	} {
		t.Run(name, func(t *testing.T) {
			script, argvLog := fakeBinary(t, "exit 0")
			useFakeBinary(t, script)
			inConfiguredDir(t)

			_, err := ExecuteHandler(ToolPrefix+tc.tool, tc.args)
			if err == nil {
				t.Fatal("invalid arguments were accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
			if _, statErr := os.Stat(argvLog); statErr == nil {
				t.Error("the binary was invoked despite invalid arguments")
			}
		})
	}
}

func TestExecuteHandler_FailsWhenAProfileHasNoReadableConfig(t *testing.T) {
	// Profile resolution is fail-fast on purpose. Falling back to the built-in
	// defaults would quietly search the wrong collection and return plausible
	// results from the wrong corpus.
	script, argvLog := fakeBinary(t, "exit 0")
	useFakeBinary(t, script)
	t.Chdir(t.TempDir()) // no .planning/.config/config.yaml here

	_, err := ExecuteHandler(ToolPrefix+"search", map[string]interface{}{
		"query":   "auth",
		"profile": "code",
	})
	if err == nil {
		t.Fatal("a profile with no config was accepted")
	}
	if !strings.Contains(err.Error(), "failed to resolve profile settings") {
		t.Errorf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(argvLog); statErr == nil {
		t.Error("the binary was invoked despite unresolvable profile settings")
	}
}

func TestStripPrefix_LeavesAnUnprefixedNameAlone(t *testing.T) {
	// A name shorter than the prefix must not be sliced — doing so would panic
	// or silently truncate. This is the guard that lets the registry be keyed on
	// bare command names.
	if got := stripPrefix(ToolPrefix + "search"); got != "search" {
		t.Errorf("stripPrefix = %q, want %q", got, "search")
	}
	if got := stripPrefix("search"); got != "search" {
		t.Errorf("stripPrefix = %q, want %q", got, "search")
	}
	if got := stripPrefix(""); got != "" {
		t.Errorf("stripPrefix = %q, want empty", got)
	}
}

// --- Tool definitions ---

func TestGetToolDefinitions_EveryToolIsPrefixedAndRoutable(t *testing.T) {
	// The MCP name and the registry key are two halves of one contract: the
	// server advertises the prefixed name, and ExecuteHandler routes on the name
	// with the prefix removed. A tool whose stripped name is not in the registry
	// is advertised but un-callable.
	tools := GetToolDefinitions()
	if len(tools) == 0 {
		t.Fatal("no tools defined")
	}

	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if !strings.HasPrefix(tool.Name, ToolPrefix) {
			t.Errorf("tool %q does not carry the %q prefix", tool.Name, ToolPrefix)
			continue
		}
		if seen[tool.Name] {
			t.Errorf("tool %q is defined twice", tool.Name)
		}
		seen[tool.Name] = true

		cmd := stripPrefix(tool.Name)
		if _, ok := commandRegistry[cmd]; !ok {
			t.Errorf("tool %q strips to %q, which has no command registry entry", tool.Name, cmd)
		}
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
	}
}

func TestGetToolDefinitions_EverySchemaIsAValidObjectSchema(t *testing.T) {
	// InputSchema is shipped to the client as raw JSON. Anything that is not a
	// well-formed object schema either fails validation at the client or lets a
	// malformed argument map through to the CLI.
	for _, tool := range GetToolDefinitions() {
		t.Run(tool.Name, func(t *testing.T) {
			var schema struct {
				Type       string                     `json:"type"`
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			}
			if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
				t.Fatalf("InputSchema is not valid JSON: %v\n%s", err, tool.InputSchema)
			}
			if schema.Type != "object" {
				t.Errorf(`schema type = %q, want "object"`, schema.Type)
			}
			if schema.Properties == nil {
				t.Errorf("schema declares no properties: %s", tool.InputSchema)
			}
			// Anything listed as required must actually be a declared property,
			// or the client rejects every call to the tool.
			for _, req := range schema.Required {
				if _, ok := schema.Properties[req]; !ok {
					t.Errorf("required field %q is not a declared property", req)
				}
			}
			// Every property must itself declare a type, which is what the
			// client uses to coerce the model's arguments.
			for name, raw := range schema.Properties {
				var prop struct {
					Type string `json:"type"`
				}
				if err := json.Unmarshal(raw, &prop); err != nil {
					t.Fatalf("property %q is not valid JSON: %v", name, err)
				}
				if prop.Type == "" {
					t.Errorf("property %q declares no type: %s", name, raw)
				}
			}
		})
	}
}

func TestGetToolDefinitions_CoversEveryRegisteredCommand(t *testing.T) {
	// The mirror of the routing check: a command in the registry with no tool
	// definition is reachable by code but invisible to every MCP client.
	advertised := make(map[string]bool)
	for _, tool := range GetToolDefinitions() {
		advertised[stripPrefix(tool.Name)] = true
	}
	for _, cmd := range RegisteredCommands() {
		if !advertised[cmd] {
			t.Errorf("registered command %q has no tool definition", cmd)
		}
	}
}
