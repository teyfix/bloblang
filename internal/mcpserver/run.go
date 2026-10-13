package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/teyfix/bloblang-lsp/internal/mapping"
)

// Use the SDK's low-level handler for evaluation: the generic adapter applies
// schema defaults by decoding to float64 and re-encoding the input. That would
// silently round large integer IDs in input, root and metadata. This handler
// validates a separate representation, then decodes the original arguments with
// json.Number and forwards the original RawMessages unchanged.
func addRunTool(server *mcp.Server, options Options) {
	definition := tool("bloblang_run", "Evaluate one Bloblang mapping and return output, metadata, deletion status, lint diagnostics and errors. Provide source or mappingPath, and input (any JSON value, including null), inputPath (raw JSON/YAML), or samplePath (LSP $bloblang sample envelope). Omit input arguments to use mapping sample directives or a file-backed sibling sample. Paths are local to this server. Use file paths for large samples. Environment and metadata fixtures are explicit; host I/O functions are disabled. timeoutMs is 1..5000 (default 5000); execution runs in a killable worker.", runSchema())
	definition.Annotations.IdempotentHint = false
	schemaBytes, err := json.Marshal(definition.InputSchema)
	if err != nil {
		panic(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		panic(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		panic(err)
	}
	server.AddTool(definition, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fail := func(err error) (*mcp.CallToolResult, error) {
			result := &mcp.CallToolResult{}
			result.SetError(err)
			return result, nil
		}
		if err := boundedArguments(request); err != nil {
			return fail(err)
		}
		var arguments any
		if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
			return fail(fmt.Errorf("invalid arguments: %w", err))
		}
		if err := resolved.Validate(arguments); err != nil {
			return fail(fmt.Errorf("invalid arguments: %w", err))
		}
		var input runRequest
		decoder := json.NewDecoder(bytes.NewReader(request.Params.Arguments))
		decoder.UseNumber()
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			return fail(fmt.Errorf("invalid arguments: %w", err))
		}
		if options.Run == nil {
			return fail(errors.New("mapping evaluator is not configured"))
		}
		timeout := input.TimeoutMS
		if timeout == 0 {
			timeout = int(mapping.DefaultTimeout / time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
		defer cancel()
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		output, err := options.Run(ctx, input.Request)
		if err != nil {
			return fail(err)
		}
		data, err := json.Marshal(output)
		if err != nil {
			return fail(fmt.Errorf("encode evaluation result: %w", err))
		}
		result := &mcp.CallToolResult{StructuredContent: json.RawMessage(data), Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}
		switch value := output.(type) {
		case mapping.Result:
			result.IsError = value.Error != nil || value.LintError != nil
		case *mapping.Result:
			result.IsError = value != nil && (value.Error != nil || value.LintError != nil)
		}
		return result, nil
	})
}
