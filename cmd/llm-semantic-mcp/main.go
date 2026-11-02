package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/samestrin/llm-semantic-axi/internal/semantic/mcpserver"
)

const (
	serverName         = "llm-semantic-mcp"
	serverInstructions = "llm-semantic-mcp provides semantic code search using local embedding models. It wraps the llm-semantic CLI with 4 tools for searching, indexing, and managing semantic code indexes."
)

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

	// Create MCP server using official SDK
	server := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, &mcp.ServerOptions{
		Instructions: serverInstructions,
	})

	// Register all tools
	tools := mcpserver.GetToolDefinitions()
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
	if err := server.Run(ctx, transport); err != nil {
		fmt.Fprintf(stderr, "Server error: %v\n", err)
		return 1
	}
	return 0
}
