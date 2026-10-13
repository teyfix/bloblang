// Package mcpserver exposes local Bloblang tools through the MCP protocol.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/teyfix/bloblang-lsp/internal/benthos"
	"github.com/teyfix/bloblang-lsp/internal/help"
	"github.com/teyfix/bloblang-lsp/internal/lsp"
	"github.com/teyfix/bloblang-lsp/internal/mapping"
)

// Options supplies the shared evaluator. Run must honor the context deadline
// with an isolated worker; a goroutine timeout cannot interrupt Bloblang.
type Options struct {
	Run func(context.Context, mapping.Request) (any, error)
}

type runRequest struct {
	mapping.Request
	TimeoutMS int `json:"timeoutMs,omitempty"`
}

type sourceRequest struct {
	Source      *string           `json:"source,omitempty"`
	MappingPath string            `json:"mappingPath,omitempty"`
	Filename    string            `json:"filename,omitempty"`
	Imports     map[string]string `json:"imports,omitempty"`
	TabSize     int               `json:"tabSize,omitempty"`
}

type lintResult struct {
	Source      string               `json:"source"`
	Diagnostics []lsp.ToolDiagnostic `json:"diagnostics"`
}

type formatResult struct {
	Source  string `json:"source"`
	Changed bool   `json:"changed"`
}

// New creates a server without opening a transport or reading any files.
// Serve it with server.Run(ctx, &mcp.StdioTransport{}) for the CLI interface.
// All tools return structured content and a JSON text fallback for older clients.
func New(version string, options Options) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "bloblang", Version: version}, &mcp.ServerOptions{
		Instructions: "Local Bloblang tools. Use bloblang_run to evaluate mappings and receive lint diagnostics; local mapping/input/sample file paths avoid copying large files into requests. Search is lexical with fuzzy identifier matching, not semantic. Batch up to 8 related queries in bloblang_search, then retrieve full documentation by exact IDs with bloblang_docs. No tool writes files.",
	})
	index := help.NewIndex(benthos.NewEnvironment())

	addRunTool(server, options)

	mcp.AddTool(server, tool("bloblang_lint", "Statically validate and lint Bloblang source or an entire YAML mapping file without executing it. Provide source or mappingPath. Optional filename gives inline source its extension and project configuration context (default mapping.blobl). Imports for inline source must be explicitly supplied. Returns source and diagnostics with one-based Unicode positions; never writes files.", sourceSchema(false)),
		func(ctx context.Context, req *mcp.CallToolRequest, input sourceRequest) (*mcp.CallToolResult, lintResult, error) {
			var output lintResult
			path, source, err := loadSource(ctx, req, input)
			if err != nil {
				return nil, output, err
			}
			tooling, err := newTooling(input)
			if err != nil {
				return nil, output, err
			}
			defer tooling.Close()
			result, err := tooling.Lint(path, source, false)
			output.Source, output.Diagnostics = result.Text, result.Diagnostics
			return nil, output, err
		})

	mcp.AddTool(server, tool("bloblang_format", "Format Bloblang source or embedded YAML mappings while preserving the YAML host. Provide source or mappingPath. Optional filename gives inline source its extension and project configuration context (default mapping.blobl). Imports for inline source must be explicitly supplied. tabSize is 1..16 (default 2). Returns formatted source and whether it changed; never writes files.", sourceSchema(true)),
		func(ctx context.Context, req *mcp.CallToolRequest, input sourceRequest) (*mcp.CallToolResult, formatResult, error) {
			var output formatResult
			path, source, err := loadSource(ctx, req, input)
			if err != nil {
				return nil, output, err
			}
			tooling, err := newTooling(input)
			if err != nil {
				return nil, output, err
			}
			defer tooling.Close()
			tabSize := input.TabSize
			if tabSize == 0 {
				tabSize = 2
			}
			output.Source, err = tooling.Format(path, source, tabSize)
			output.Changed = output.Source != source
			return nil, output, err
		})

	mcp.AddTool(server, tool("bloblang_search", "Search installed Bloblang function and method documentation with 1..8 batched queries. Search is lexical with typo-tolerant fuzzy identifier matching; semantic search is not supported. Send multiple alternative terms in one request rather than repeating searches. Results are deduplicated and carry per-query ranks and match evidence (field, terms, excerpt and edit distance where relevant). limitPerQuery is 1..10 (default 5). Use canonical result IDs with bloblang_docs for full signatures and examples.", searchSchema()),
		func(ctx context.Context, _ *mcp.CallToolRequest, input help.SearchRequest) (*mcp.CallToolResult, help.SearchResponse, error) {
			if err := ctx.Err(); err != nil {
				return nil, help.SearchResponse{}, err
			}
			output, err := index.Search(input)
			return nil, output, err
		})
	mcp.AddTool(server, tool("bloblang_docs", "Retrieve complete installed Bloblang documentation, parameters, categories and examples for 1..16 exact canonical IDs such as method:uppercase or function:env. Get IDs from bloblang_search. Missing IDs are reported individually; deprecated entries require includeDeprecated. Examples are returned as documentation and are not executed.", docsSchema()),
		func(ctx context.Context, _ *mcp.CallToolRequest, input help.GetRequest) (*mcp.CallToolResult, help.GetResponse, error) {
			if err := ctx.Err(); err != nil {
				return nil, help.GetResponse{}, err
			}
			output, err := index.Get(input)
			return nil, output, err
		})
	return server
}

func tool(name, description string, schema map[string]any) *mcp.Tool {
	closed := false
	return &mcp.Tool{Name: name, Description: description, InputSchema: schema,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closed, DestructiveHint: &closed}}
}

func boundedArguments(request *mcp.CallToolRequest) error {
	if len(request.Params.Arguments) > mapping.MaxRequestBytes {
		return fmt.Errorf("tool arguments exceed %d bytes", mapping.MaxRequestBytes)
	}
	return nil
}

func newTooling(input sourceRequest) (*lsp.Tooling, error) {
	imports := input.Imports
	if input.Source != nil && imports == nil {
		imports = map[string]string{}
	}
	return lsp.NewToolingWithImports(imports)
}

func loadSource(ctx context.Context, request *mcp.CallToolRequest, input sourceRequest) (string, string, error) {
	if err := boundedArguments(request); err != nil {
		return "", "", err
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if input.Source != nil {
		path := input.Filename
		if path == "" {
			path = "mapping.blobl"
		}
		return path, *input.Source, nil
	}
	if input.Filename != "" {
		return "", "", errors.New("filename is only supported with inline source")
	}
	if strings.TrimSpace(input.MappingPath) == "" {
		return "", "", errors.New("mappingPath must not be empty")
	}
	// Reject directories/devices/FIFOs before opening: opening a FIFO could
	// block indefinitely before there is an opportunity to inspect its mode.
	info, err := os.Stat(input.MappingPath)
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() {
		return "", "", errors.New("mappingPath must be a regular file")
	}
	f, err := os.Open(input.MappingPath)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, mapping.MaxRequestBytes+1))
	if err != nil {
		return "", "", err
	}
	if len(data) > mapping.MaxRequestBytes {
		return "", "", fmt.Errorf("mapping file exceeds %d bytes", mapping.MaxRequestBytes)
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	return input.MappingPath, string(data), nil
}
