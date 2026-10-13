package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/teyfix/bloblang-lsp/internal/mapping"
	"github.com/teyfix/bloblang-lsp/internal/schema"
)

type schemaRequest struct {
	Input      json.RawMessage `json:"input,omitempty"`
	InputPath  string          `json:"inputPath,omitempty"`
	SamplePath string          `json:"samplePath,omitempty"`
	MapPaths   []string        `json:"mapPaths,omitempty"`
}

func addSchemaTool(server *mcp.Server) {
	inputSchema := object(map[string]any{
		"input":      map[string]any{},
		"inputPath":  pathSchema(),
		"samplePath": pathSchema(),
		"mapPaths":   map[string]any{"type": "array", "items": textSchema(4096), "maxItems": schema.MaxMapPaths},
	})
	inputSchema["oneOf"] = exclusiveRequired("input", "inputPath", "samplePath")
	definition := tool("bloblang_schema", "Summarize a JSON or YAML sample as JSON Type Definition (JTD) using an existing Go inference library. Supply input (any JSON value), inputPath (raw JSON/YAML), or samplePath (LSP $bloblang envelope; summarizes its input). File paths avoid copying large samples. Optional mapPaths are JSON Pointers identifying objects with dynamic keys (use '-' for array elements). The result contains schema and meta warnings: this is sample-based evidence, not an authoritative contract; enums are disabled, numbers use float64, and empty/mixed values may be unconstrained. No mapping executes and no files are written.", inputSchema)
	// json.RawMessage must be exposed as an object, not the base64 string schema
	// inferred from its []byte representation.
	definition.OutputSchema = object(map[string]any{
		"schema": map[string]any{"type": "object"},
		"meta":   map[string]any{"type": "object"},
	})
	definition.OutputSchema.(map[string]any)["required"] = []string{"schema", "meta"}
	mcp.AddTool(server, definition, func(ctx context.Context, request *mcp.CallToolRequest, _ schemaRequest) (*mcp.CallToolResult, schema.Result, error) {
		var output schema.Result
		if err := boundedArguments(request); err != nil {
			return nil, output, err
		}
		if err := ctx.Err(); err != nil {
			return nil, output, err
		}
		// Use the original arguments, preserving numeric input through the
		// shared loader. Inference then intentionally normalizes numeric types.
		var input schemaRequest
		decoder := json.NewDecoder(bytes.NewReader(request.Params.Arguments))
		decoder.UseNumber()
		if err := decoder.Decode(&input); err != nil {
			return nil, output, err
		}
		options := schema.Options{}
		for _, pointer := range input.MapPaths {
			path, err := schema.ParseMapPath(pointer)
			if err != nil {
				return nil, output, fmt.Errorf("mapPaths: %w", err)
			}
			options.MapPaths = append(options.MapPaths, path)
		}
		resolved, err := mapping.Resolve(mapping.Request{Source: "root = this", Input: input.Input, InputPath: input.InputPath, SamplePath: input.SamplePath})
		if err != nil {
			return nil, output, err
		}
		var value any
		decoder = json.NewDecoder(bytes.NewReader(resolved.Input))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, output, err
		}
		output, err = schema.Infer(value, options)
		if err == nil {
			err = ctx.Err()
		}
		return nil, output, err
	})
}
