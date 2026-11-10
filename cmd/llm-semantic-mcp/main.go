package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/samestrin/llm-semantic-axi/internal/semantic/mcpserver"
)

const serverName = "llm-semantic-mcp"

// serverInstructions takes the tool count from the definitions it describes.
// It was a literal saying "4 tools" while 16 were registered.
func serverInstructions(toolCount int) string {
	return fmt.Sprintf("llm-semantic-mcp provides semantic code search using local embedding models. It wraps the llm-semantic CLI with %d tools for searching, indexing, and managing semantic code indexes.", toolCount)
}

// codeServerClosing is the JSON-RPC code go-sdk gives its internal
// ErrServerClosing, which it does not export by name.
const codeServerClosing = -32004

// serverVersion is a var, not a const, so the build can stamp it with
// -X main.serverVersion. A const cannot be set by ldflags: the linker rewrites
// a symbol's data, and a const has no symbol to rewrite. It was a const with a
// hardcoded "1.6.0" while the repo was inside llm-tools, which is why the
// reported version drifted from the tag.
var serverVersion = "dev"

func main() {
	// A reply written after the client has closed our stdout would otherwise
	// kill the process with SIGPIPE, which Go raises for a broken pipe on fd 1
	// and a supervisor reads as a crash. Ignored, the write returns EPIPE and
	// run() can see that the client went away.
	signal.Ignore(syscall.SIGPIPE)
	if printVersion(os.Args[1:], os.Stdout) {
		os.Exit(0)
	}
	os.Exit(run(context.Background(), os.Stdin, nopCloser{os.Stdout}, os.Stderr))
}

// printVersion handles -v, -V and --version: it writes the bare version and
// reports true so main exits 0 before run() checks for the CLI binary or reads
// any JSON-RPC from stdin.
func printVersion(args []string, stdout io.Writer) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "-v", "-V", "--version":
		fmt.Fprintln(stdout, serverVersion)
		return true
	}
	return false
}

// startedBanner adds the "v" itself only when the stamped version lacks one:
// release builds stamp "v1.2.0", which printed as "vv1.2.0".
func startedBanner(version string, toolCount int) string {
	return fmt.Sprintf("%s v%s started with %d tools", serverName, strings.TrimPrefix(version, "v"), toolCount)
}

// nopCloser keeps stdout open when the SDK closes the connection, as
// mcp.StdioTransport does.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// recordingWriter remembers the first error its writer returned. When a write
// fails the SDK closes the connection, and server.Run then reports the closed
// reader instead of the write; this keeps the cause.
type recordingWriter struct {
	io.WriteCloser
	mu  sync.Mutex
	err error
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	n, err := w.WriteCloser.Write(p)
	if err != nil {
		w.mu.Lock()
		if w.err == nil {
			w.err = err
		}
		w.mu.Unlock()
	}
	return n, err
}

func (w *recordingWriter) writeErr() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// run is the whole server: it checks the CLI is installed, registers every
// tool, serves MCP over in and out until the session ends, and returns the
// process exit code. It is split from main() so a test can drive a full
// session and read the exit decision without os.Exit ending the test binary.
func run(ctx context.Context, in io.ReadCloser, out io.WriteCloser, stderr io.Writer) int {
	// Verify llm-semantic binary exists
	if _, err := os.Stat(mcpserver.BinaryPath); os.IsNotExist(err) {
		fmt.Fprintf(stderr, "ERROR: llm-semantic binary not found at %s\n", mcpserver.BinaryPath)
		fmt.Fprintf(stderr, "Please ensure llm-semantic is installed and accessible.\n")
		return 1
	}

	tools := mcpserver.GetToolDefinitions()

	// Create MCP server using official SDK
	server := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, &mcp.ServerOptions{
		Instructions: serverInstructions(len(tools)),
	})

	// Register all tools
	for _, toolDef := range tools {
		// Capture for closure
		td := toolDef
		server.AddTool(&mcp.Tool{
			Name:        td.Name,
			Description: td.Description,
			InputSchema: td.InputSchema,
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			// Unmarshal arguments
			var args map[string]interface{}
			if req.Params.Arguments != nil {
				if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
					return &mcp.CallToolResult{
						Content: []mcp.Content{
							&mcp.TextContent{Text: "Error parsing arguments: " + err.Error()},
						},
						IsError: true,
					}, nil
				}
			}

			// Execute the tool using the handler
			output, err := mcpserver.ExecuteHandler(td.Name, args)
			if err != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{Text: "Error: " + err.Error()},
					},
					IsError: true,
				}, nil
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: output},
				},
			}, nil
		})
	}

	// Log startup
	fmt.Fprintln(stderr, startedBanner(serverVersion, len(tools)))

	// Serve until the session ends
	w := &recordingWriter{WriteCloser: out}
	runErr := server.Run(ctx, &mcp.IOTransport{Reader: in, Writer: w})
	if err := sessionResult(runErr, w.writeErr()); err != nil {
		fmt.Fprintf(stderr, "Server error: %v\n", err)
		return 1
	}
	return 0
}

// sessionResult decides how a session ended: nil when the client simply went
// away, which is how every session ends, or the error to report and exit 1 on.
// writeErr is the first error from the server's output, if any.
func sessionResult(runErr, writeErr error) error {
	if writeErr != nil {
		// The SDK closed the connection because a write failed, so runErr only
		// says the reader was closed. A broken pipe means the client stopped
		// reading; any other write failure is the server's and is the cause.
		if errors.Is(writeErr, syscall.EPIPE) {
			return nil
		}
		return writeErr
	}
	if runErr == nil || replyAfterHangUp(runErr) {
		return nil
	}
	return runErr
}

// replyAfterHangUp reports whether runErr is the SDK refusing a write because
// the read side had already hit EOF, as "server is closing: EOF": a reply
// that lost the race against the client's hang-up. A bare io.EOF never
// arrives; the SDK drops it. The error formats its cause with %v, so
// errors.Is cannot see the EOF inside and the code is the only thing to match
// on. That is safe because the SDK returns a broken read side ahead of it: a
// peer that sent garbage still surfaces as its own error and still exits 1.
func replyAfterHangUp(runErr error) bool {
	var wireErr *jsonrpc.Error
	return errors.As(runErr, &wireErr) && wireErr.Code == codeServerClosing
}
