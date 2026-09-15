package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/samestrin/llm-semantic-axi/internal/semantic/mcpserver"
)

// main() is the whole server: it checks the CLI is installed, registers every
// tool definition behind one closure, and serves MCP over stdio. It is driven
// here end to end by pointing mcpserver.BinaryPath at a fake CLI, swapping
// os.Stdin/os.Stdout for pipes, and connecting a real MCP client to the other
// ends. Closing the client's transport gives the server EOF, which is how
// main() returns instead of blocking forever.

// fakeCLI writes an executable stand-in for llm-semantic that echoes payload,
// and points mcpserver.BinaryPath at it for the duration of the test.
func fakeCLI(t *testing.T, payload string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake CLI is a POSIX shell script")
	}
	script := filepath.Join(t.TempDir(), "llm-semantic")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' '"+payload+"'\n"), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	original := mcpserver.BinaryPath
	mcpserver.BinaryPath = script
	t.Cleanup(func() { mcpserver.BinaryPath = original })
}

// startServer runs main() against piped stdio and returns a connected client
// session. The session is closed and main()'s return is awaited on cleanup.
func startServer(t *testing.T) *mcp.ClientSession {
	t.Helper()

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}

	realStdin, realStdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = stdinR, stdoutW

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		main()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: stdoutR, Writer: stdinW}, nil)
	if err != nil {
		os.Stdin, os.Stdout = realStdin, realStdout
		t.Fatalf("Connect: %v", err)
	}

	// NOTHING here is closed — not the session, not either pipe — and main()'s
	// return is not awaited. That looks careless and is the opposite.
	//
	// main() treats ANY error from server.Run as fatal and calls os.Exit(1).
	// Every possible teardown produces one: closing the client session or the
	// write end gives the server "server is closing: EOF", and closing a read
	// end gives it "read |0: file already closed". os.Exit from this goroutine
	// takes the whole test binary down with it, so there is no shutdown
	// sequence that leaves the process alive. Both variants were measured, and
	// both failed.
	//
	// It raced locally and lost in CI: on Linux the server reached os.Exit
	// before the test finished and the package failed in 0.026s. Exiting 1 on a
	// normal client disconnect is a real defect, filed in the debt record. This
	// test parks the server instead of pretending the behaviour is correct.
	//
	// The cost is a leaked goroutine and four descriptors per test, reclaimed
	// when the test binary exits. Nothing is lost from the assertions:
	// registration, the stamped version, the tool call, the tool-error shape
	// and the schema round trip all happen while the session is live.
	t.Cleanup(func() {
		os.Stdin, os.Stdout = realStdin, realStdout
	})
	_ = returned

	return session
}

func TestServer_AdvertisesEveryToolDefinition(t *testing.T) {
	// The tools the server registers are what the model can call. Registering a
	// subset — or registering them all under one shared closure that captures
	// the wrong loop variable — is invisible until a specific tool is invoked.
	fakeCLI(t, "{}")
	session := startServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	want := mcpserver.GetToolDefinitions()
	if len(res.Tools) != len(want) {
		t.Errorf("server advertises %d tools, GetToolDefinitions returns %d", len(res.Tools), len(want))
	}
	advertised := make(map[string]bool, len(res.Tools))
	for _, tool := range res.Tools {
		advertised[tool.Name] = true
	}
	for _, def := range want {
		if !advertised[def.Name] {
			t.Errorf("tool %q was defined but not registered", def.Name)
		}
	}
}

func TestServer_ReportsTheStampedVersion(t *testing.T) {
	// serverVersion is a var precisely so -X main.serverVersion can stamp it;
	// it was a const with a hardcoded number inside llm-tools, which is how the
	// reported version drifted from the tag. The client reads it at initialize.
	fakeCLI(t, "{}")
	session := startServer(t)

	impl := session.InitializeResult().ServerInfo
	if impl.Name != serverName {
		t.Errorf("server name = %q, want %q", impl.Name, serverName)
	}
	if impl.Version != serverVersion {
		t.Errorf("server version = %q, want the stamped %q", impl.Version, serverVersion)
	}
}

func TestServer_ToolCallReturnsTheCLIOutput(t *testing.T) {
	// The registered closure is the only thing between a tool call and the CLI.
	// Its job is to hand the subprocess's stdout back as text content, and the
	// per-tool capture (`td := toolDef`) is what keeps every tool from running
	// whichever definition the loop finished on.
	const payload = `{"chunks":12,"collection":"llm_code"}`
	fakeCLI(t, payload)
	session := startServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      mcpserver.ToolPrefix + "index_status",
		Arguments: map[string]any{"storage": "sqlite"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool call reported an error: %+v", res.Content)
	}
	if len(res.Content) != 1 {
		t.Fatalf("got %d content blocks, want 1: %+v", len(res.Content), res.Content)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content block is %T, want *mcp.TextContent", res.Content[0])
	}
	if text.Text != payload {
		t.Errorf("tool output = %q, want the CLI's stdout %q", text.Text, payload)
	}
}

func TestServer_ToolFailureIsReportedAsAToolErrorNotATransportError(t *testing.T) {
	// A failing CLI must come back as IsError with the diagnosis in the content,
	// not as a protocol-level error. The distinction matters: a transport error
	// tears the session down, while a tool error lets the model read what went
	// wrong and try something else.
	fakeCLI(t, "{}")
	session := startServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// A search with no query fails validation before anything is executed.
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      mcpserver.ToolPrefix + "search",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool returned a transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("an invalid tool call was reported as success: %+v", res.Content)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content block is %T, want *mcp.TextContent", res.Content[0])
	}
	if !strings.HasPrefix(text.Text, "Error: ") {
		t.Errorf("error content %q is not prefixed as an error", text.Text)
	}
	if !strings.Contains(text.Text, "non-empty query") {
		t.Errorf("error content %q does not say what was wrong", text.Text)
	}
}

func TestServer_EveryAdvertisedSchemaSurvivesTheRoundTrip(t *testing.T) {
	// main() passes InputSchema through as raw JSON. Anything malformed would be
	// rejected or flattened by the SDK on the way out, so this checks what the
	// client actually receives rather than what the definitions hold.
	fakeCLI(t, "{}")
	session := startServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range res.Tools {
		if tool.InputSchema == nil {
			t.Errorf("tool %q arrived with no input schema", tool.Name)
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Errorf("tool %q schema will not re-marshal: %v", tool.Name, err)
			continue
		}
		var schema struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Errorf("tool %q schema is not a JSON object: %v", tool.Name, err)
			continue
		}
		if schema.Type != "object" {
			t.Errorf(`tool %q schema type = %q, want "object"`, tool.Name, schema.Type)
		}
		if len(schema.Properties) == 0 {
			t.Errorf("tool %q arrived with no properties: %s", tool.Name, raw)
		}
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("tool %q arrived with no description", tool.Name)
		}
	}
}
