package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

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
	os.Exit(run(context.Background(), &mcp.StdioTransport{}, os.Stderr))
}

// run is the whole server: it checks the CLI is installed, registers every
// tool, serves MCP on transport until the session ends, and returns the
// process exit code. It is split from main() so a test can drive a full
// session and read the exit decision without os.Exit ending the test binary.
func run(ctx context.Context, transport mcp.Transport, stderr io.Writer) int {
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
	fmt.Fprintf(stderr, "%s v%s started with %d tools\n", serverName, serverVersion, len(tools))

	// Serve until the session ends
	if err := server.Run(ctx, transport); err != nil && !clientDisconnected(err) {
		fmt.Fprintf(stderr, "Server error: %v\n", err)
		return 1
	}
	return 0
}

// clientDisconnected reports whether a server.Run error only says the client
// went away, which is how every session ends and not a failure.
//
// A bare io.EOF never reaches here: the SDK drops it. What does arrive is the
// SDK refusing a write because the read side had already hit EOF, as
// "server is closing: EOF" — a reply that lost the race against the client's
// hang-up. That error formats its cause with %v, so errors.Is cannot see the
// EOF inside and the code is the only thing to match on. It is safe to treat
// as clean because the SDK returns a broken read side ahead of it: a peer
// that sent garbage still surfaces as its own error and still exits 1.
func clientDisconnected(err error) bool {
	var wireErr *jsonrpc.Error
	return errors.As(err, &wireErr) && wireErr.Code == codeServerClosing
}
