package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/samestrin/llm-semantic-axi/internal/semantic/mcpserver"
)

// run() is the whole server: it checks the CLI is installed, registers every
// tool definition behind one closure, and serves MCP until the session ends.
// It is driven here end to end by pointing mcpserver.BinaryPath at a fake CLI
// and connecting a real MCP client to the other ends of two pipes. Because
// run() returns an exit code instead of calling os.Exit, every test can end
// its session and read what the process would have exited with.

// initializeRequest is a raw MCP initialize call, for tests that drive the
// server's input directly instead of through a client.
const initializeRequest = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
	`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}` + "\n"

// runMainEnv makes the test binary act as the server: TestMain calls main()
// instead of running tests, so a test can exec it with a real stdin and
// stdout and observe the process exit the way a supervisor would.
const runMainEnv = "LLM_SEMANTIC_MCP_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

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

// server is one run() of the MCP server, connected to a client session.
type server struct {
	session *mcp.ClientSession
	// clientWrite is the client's end of the server's input pipe, exposed so a
	// test can write raw bytes to the server or close the pipe directly.
	clientWrite *os.File
	done        chan struct{}
	code        int
	stderr      bytes.Buffer
}

// wait blocks until run() returns, then reports its exit code and stderr.
// Reading code and stderr only after done is closed is what makes them safe
// to read without a lock.
func (s *server) wait(t *testing.T) (int, string) {
	t.Helper()
	select {
	case <-s.done:
		return s.code, s.stderr.String()
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not return after the client went away")
		return 0, ""
	}
}

// launch runs run() on a pair of pipes and returns the server with its input
// pipe's write end. It does not connect a client.
func launch(t *testing.T) (*server, *os.File, *os.File) {
	t.Helper()

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	t.Cleanup(func() {
		for _, f := range []*os.File{stdinR, stdinW, stdoutR, stdoutW} {
			_ = f.Close()
		}
	})

	srv := &server{clientWrite: stdinW, done: make(chan struct{})}
	go func() {
		defer close(srv.done)
		srv.code = run(context.Background(), &mcp.IOTransport{Reader: stdinR, Writer: stdoutW}, &srv.stderr)
	}()
	return srv, stdoutR, stdinW
}

// startServer runs the server and returns it with a connected client session.
// On cleanup the session is closed and run() must return 0: every test that
// uses this helper therefore also checks that a normal disconnect is a clean
// exit, which is the shutdown the server previously turned into exit 1.
func startServer(t *testing.T) *server {
	t.Helper()

	srv, stdoutR, stdinW := launch(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: stdoutR, Writer: stdinW}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	srv.session = session

	t.Cleanup(func() {
		_ = session.Close()
		if code, stderr := srv.wait(t); code != 0 {
			t.Errorf("run() = %d after the client disconnected, want 0; stderr:\n%s", code, stderr)
		}
	})
	return srv
}

func TestServer_AdvertisesEveryToolDefinition(t *testing.T) {
	// The tools the server registers are what the model can call. Registering a
	// subset — or registering them all under one shared closure that captures
	// the wrong loop variable — is invisible until a specific tool is invoked.
	fakeCLI(t, "{}")
	session := startServer(t).session

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
	session := startServer(t).session

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
	session := startServer(t).session

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
	session := startServer(t).session

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
	session := startServer(t).session

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

func TestServer_ClientDisconnectIsACleanExit(t *testing.T) {
	// A client hanging up is how every MCP session ends. The server used to
	// treat the resulting "server is closing: EOF" from server.Run as fatal and
	// exit 1, so a supervisor saw each healthy session end as a crash.
	fakeCLI(t, "{}")
	srv := startServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := srv.session.ListTools(ctx, nil); err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	if err := srv.session.Close(); err != nil {
		t.Fatalf("session.Close: %v", err)
	}
	code, stderr := srv.wait(t)
	if code != 0 {
		t.Errorf("run() = %d after the client closed its session, want 0", code)
	}
	if strings.Contains(stderr, "Server error") {
		t.Errorf("a normal disconnect was logged as a server error:\n%s", stderr)
	}
	if !strings.Contains(stderr, "started with") {
		t.Errorf("stderr is missing the startup line, so the server never ran:\n%s", stderr)
	}
}

func TestServer_ClosedInputIsACleanExit(t *testing.T) {
	// A client process that exits without an MCP goodbye just closes the
	// server's stdin. That is still a normal end of session, not a failure.
	fakeCLI(t, "{}")
	srv := startServer(t)

	if err := srv.clientWrite.Close(); err != nil {
		t.Fatalf("close the server's input: %v", err)
	}
	code, stderr := srv.wait(t)
	if code != 0 {
		t.Errorf("run() = %d after its input was closed, want 0", code)
	}
	if strings.Contains(stderr, "Server error") {
		t.Errorf("a closed input was logged as a server error:\n%s", stderr)
	}
}

func TestServer_HangUpBeforeTheReplyIsACleanExit(t *testing.T) {
	// This is the disconnect CI caught: the client sends a request and goes
	// away before the server answers. The server's reply then finds the read
	// side at EOF, the SDK records "server is closing: EOF" as a write error,
	// and server.Run returns it. Nothing failed, and the process used to exit 1.
	// The race goes the server's way only occasionally, so it is run 20 times.
	fakeCLI(t, "{}")
	for i := 0; i < 20; i++ {
		srv, stdoutR, stdinW := launch(t)
		go func() { _, _ = io.Copy(io.Discard, stdoutR) }()

		if _, err := stdinW.Write([]byte(initializeRequest)); err != nil {
			t.Fatalf("write initialize: %v", err)
		}
		if err := stdinW.Close(); err != nil {
			t.Fatalf("close the server's input: %v", err)
		}
		code, stderr := srv.wait(t)
		if code != 0 || strings.Contains(stderr, "Server error") {
			t.Fatalf("attempt %d: run() = %d after the client hung up mid-request, want 0; stderr:\n%s", i, code, stderr)
		}
	}
}

func TestServer_ReplyToAClientThatStoppedReadingIsACleanExit(t *testing.T) {
	// The other side of the same race, and the one CI hit second: the client
	// closes its session, which closes the end it reads from, while the
	// server still has a reply to write. The write fails with EPIPE, the SDK
	// returns that, and run() exited 1 with "write |1: broken pipe". The
	// reader going away is the client disconnecting, not the server failing.
	fakeCLI(t, "{}")
	for i := 0; i < 5; i++ {
		srv, stdoutR, stdinW := launch(t)
		if err := stdoutR.Close(); err != nil {
			t.Fatalf("close the client's read end: %v", err)
		}
		if _, err := stdinW.Write([]byte(initializeRequest)); err != nil {
			t.Fatalf("write initialize: %v", err)
		}
		// Give the reply time to hit the closed pipe before the input ends,
		// so this exercises EPIPE rather than the closing-on-EOF path.
		time.Sleep(50 * time.Millisecond)
		if err := stdinW.Close(); err != nil {
			t.Fatalf("close the server's input: %v", err)
		}
		code, stderr := srv.wait(t)
		if code != 0 || strings.Contains(stderr, "Server error") {
			t.Fatalf("attempt %d: run() = %d after the client stopped reading, want 0; stderr:\n%s", i, code, stderr)
		}
	}
}

func TestBinary_ClientThatStoppedReadingIsACleanExit(t *testing.T) {
	// In the real binary that broken pipe is on fd 1, where Go does not return
	// EPIPE at all: it kills the process with SIGPIPE, so the disconnect above
	// was reported to a supervisor as a crash before run() saw an error. This
	// re-executes the test binary as the server, so stdout is a real fd 1.
	if runtime.GOOS == "windows" {
		t.Skip("SIGPIPE and the fake CLI are POSIX")
	}
	fakeCLI(t, "{}")

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(),
		runMainEnv+"=1",
		"PATH="+filepath.Dir(mcpserver.BinaryPath)+string(os.PathListSeparator)+os.Getenv("PATH"))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = stdout.Close()
	if _, err := stdin.Write([]byte(initializeRequest)); err != nil {
		t.Fatalf("write initialize: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	_ = stdin.Close()

	err = cmd.Wait()
	if err != nil {
		t.Errorf("server exited with %v after the client stopped reading, want exit 0; stderr:\n%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "started with") {
		t.Errorf("stderr is missing the startup line, so the server never ran:\n%s", stderr.String())
	}
}

func TestServer_TransportFailureStillExitsOne(t *testing.T) {
	// The clean-exit rule must not swallow real failures. A peer that sends
	// something that is not JSON-RPC has broken the transport, and the process
	// has to say so with exit 1 and the reason on stderr.
	fakeCLI(t, "{}")
	srv, _, stdinW := launch(t)

	if _, err := stdinW.Write([]byte("this is not json-rpc\n")); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	code, stderr := srv.wait(t)
	if code != 1 {
		t.Errorf("run() = %d after a malformed message, want 1", code)
	}
	if !strings.Contains(stderr, "Server error") {
		t.Errorf("a transport failure was not reported on stderr:\n%s", stderr)
	}
}

func TestServer_MissingCLIExitsOne(t *testing.T) {
	// Without llm-semantic every tool call would fail. The server must refuse
	// to start, not come up advertising tools it cannot run.
	original := mcpserver.BinaryPath
	mcpserver.BinaryPath = filepath.Join(t.TempDir(), "no-such-llm-semantic")
	t.Cleanup(func() { mcpserver.BinaryPath = original })

	var stderr bytes.Buffer
	code := run(context.Background(), &mcp.IOTransport{Reader: os.Stdin, Writer: os.Stdout}, &stderr)
	if code != 1 {
		t.Errorf("run() = %d with the CLI missing, want 1", code)
	}
	if !strings.Contains(stderr.String(), "llm-semantic binary not found") {
		t.Errorf("stderr does not say the CLI is missing:\n%s", stderr.String())
	}
}

func TestServer_InstructionsStateTheRegisteredToolCount(t *testing.T) {
	// The instructions are the model's first description of the server. They
	// said "4 tools" while 16 were registered, so the model was told the
	// surface was a quarter of its real size. This reads what the client
	// actually receives and pins it to the definitions, so it cannot drift.
	fakeCLI(t, "{}")
	srv := startServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := srv.session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	instructions := srv.session.InitializeResult().Instructions
	want := fmt.Sprintf("with %d tools", len(res.Tools))
	if !strings.Contains(instructions, want) {
		t.Errorf("instructions do not say %q though %d tools are registered:\n%s", want, len(res.Tools), instructions)
	}
	if n := len(mcpserver.GetToolDefinitions()); n != len(res.Tools) {
		t.Errorf("GetToolDefinitions has %d tools, the client sees %d", n, len(res.Tools))
	}
}
