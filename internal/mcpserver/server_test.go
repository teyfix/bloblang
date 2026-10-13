package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/teyfix/bloblang-lsp/internal/help"
	"github.com/teyfix/bloblang-lsp/internal/mapping"
	"github.com/teyfix/bloblang-lsp/internal/schema"
)

func connect(t *testing.T, options Options) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := New("test", options).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func call(t *testing.T, client *mcp.ClientSession, name string, arguments any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return result
}

func decode[T any](t *testing.T, result *mcp.CallToolResult) T {
	t.Helper()
	if result.IsError {
		data, _ := json.Marshal(result.Content)
		t.Fatalf("tool error: %s", data)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output T
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("want JSON text fallback, got %#v", result.Content)
	}
	block, ok := result.Content[0].(*mcp.TextContent)
	if !ok || !json.Valid([]byte(block.Text)) {
		t.Fatalf("want JSON text fallback, got %#v", result.Content)
	}
	return output
}

func TestToolDiscovery(t *testing.T) {
	client := connect(t, Options{})
	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"bloblang_run": true, "bloblang_lint": true, "bloblang_format": true, "bloblang_search": true, "bloblang_docs": true, "bloblang_schema": true}
	for _, tool := range tools.Tools {
		if !want[tool.Name] {
			t.Fatalf("unexpected tool %s", tool.Name)
		}
		delete(want, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Fatalf("missing local/read-only annotations: %+v", tool)
		}
		if tool.Name == "bloblang_run" {
			if tool.Annotations.IdempotentHint {
				t.Fatal("evaluation can use random and clock functions")
			}
			schema := tool.InputSchema.(map[string]any)
			properties := schema["properties"].(map[string]any)
			if len(properties["input"].(map[string]any)) != 0 || len(properties["root"].(map[string]any)) != 0 {
				t.Fatalf("JSON value schemas must permit objects, arrays, scalars and null: %v", properties)
			}
		}
		if tool.Name == "bloblang_search" && (!strings.Contains(tool.Description, "semantic search is not supported") || !strings.Contains(tool.Description, "batched")) {
			t.Fatalf("search must advertise capabilities before calls: %s", tool.Description)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v", want)
	}
}

func TestRunInputsAndDeadline(t *testing.T) {
	requests := make(chan mapping.Request, 10)
	deadlines := make(chan time.Duration, 10)
	client := connect(t, Options{Run: func(ctx context.Context, request mapping.Request) (any, error) {
		requests <- request
		deadline, ok := ctx.Deadline()
		if !ok {
			return nil, fmt.Errorf("missing deadline")
		}
		deadlines <- time.Until(deadline)
		return map[string]any{"output": "done", "diagnostics": []any{}, "deleted": false, "metadata": map[string]any{}}, nil
	}})
	for _, input := range []string{`null`, `{"nested":[1,true,"x"]}`, `[1,2]`, `"text"`, `12`, `false`, `9007199254740993`, `{"id":9223372036854775807}`} {
		t.Run(input, func(t *testing.T) {
			result := call(t, client, "bloblang_run", json.RawMessage(`{"source":"root = this", "input":`+input+`, "timeoutMs":1200}`))
			decode[map[string]any](t, result)
			request := <-requests
			if string(request.Input) != input || request.Source != "root = this" {
				t.Fatalf("request changed: %+v", request)
			}
			if remaining := <-deadlines; remaining <= 0 || remaining > 1200*time.Millisecond {
				t.Fatalf("wrong timeout: %v", remaining)
			}
		})
	}
	result := call(t, client, "bloblang_run", map[string]any{
		"mappingPath": "/workspace/map.blobl", "samplePath": "/workspace/large-sample.yaml",
		"metadata": map[string]any{"n": json.Number("9007199254740993")}, "env": map[string]string{"REGION": "eu"},
		"imports": map[string]string{"common.blobl": "map common { root = this }"}, "root": map[string]any{"seed": json.Number("9007199254740993")},
	})
	decode[map[string]any](t, result)
	request := <-requests
	if request.MappingPath != "/workspace/map.blobl" || request.SamplePath != "/workspace/large-sample.yaml" || request.Env["REGION"] != "eu" || len(request.Imports) != 1 || request.Metadata["n"] == nil || len(request.Root) == 0 || len(request.Input) != 0 {
		t.Fatalf("file and fixture options were not forwarded: %+v", request)
	}
	if remaining := <-deadlines; remaining <= 0 || remaining > 5*time.Second {
		t.Fatalf("wrong default timeout: %v", remaining)
	}
	if request.Metadata["n"] != json.Number("9007199254740993") {
		t.Fatalf("metadata numeric precision lost: %+v", request.Metadata)
	}
	if string(request.Root) != `{"seed":9007199254740993}` {
		t.Fatalf("root numeric precision lost: %s", request.Root)
	}
}

func TestToolArgumentValidation(t *testing.T) {
	called := false
	client := connect(t, Options{Run: func(context.Context, mapping.Request) (any, error) {
		called = true
		return nil, nil
	}})
	for _, raw := range []string{
		`{}`, `{"source":"root=this","input":null,"inputPath":"x"}`,
		`{"source":"root=this","mappingPath":"x","input":null}`, `{"source":"root=this","input":null,"timeoutMs":0}`,
		`{"source":"root=this","input":null,"timeoutMs":5001}`, `{"source":"root=this","input":null,"timeoutMs":1.5}`,
		`{"source":"root=this","input":null,"env":{"x":2}}`, `{"source":"root=this","input":null,"metadata":[]}`,
		`{"source":"root=this","input":null,"imports":{"a":false}}`, `{"source":"root=this","input":null,"unknown":true}`,
	} {
		t.Run(raw, func(t *testing.T) {
			if result := call(t, client, "bloblang_run", json.RawMessage(raw)); !result.IsError {
				t.Fatalf("expected validation failure: %#v", result)
			}
		})
	}
	if called {
		t.Fatal("invalid request reached evaluator")
	}
	for _, tool := range []string{"bloblang_format", "bloblang_lint"} {
		if result := call(t, client, tool, map[string]any{"source": "root=this", "mappingPath": "x"}); !result.IsError {
			t.Fatal("conflicting source inputs accepted")
		}
	}
	if result := call(t, client, "bloblang_format", map[string]any{"source": "root=this", "tabSize": 9}); !result.IsError {
		t.Fatal("invalid tabSize accepted")
	}
}

func TestRunErrors(t *testing.T) {
	for _, result := range []mapping.Result{
		{Metadata: map[string]any{}, Error: &mapping.Error{Kind: "parse", Message: "invalid mapping"}},
		{Metadata: map[string]any{}, LintError: &mapping.Error{Kind: "lint", Message: "invalid lint configuration"}},
	} {
		client := connect(t, Options{Run: func(context.Context, mapping.Request) (any, error) { return result, nil }})
		response := call(t, client, "bloblang_run", map[string]any{"source": "root = (", "input": nil})
		if !response.IsError || response.StructuredContent == nil || len(response.Content) == 0 {
			t.Fatalf("execution/lint errors must preserve structured result and mark isError: %+v", response)
		}
	}
}

func TestSampleSchema(t *testing.T) {
	client := connect(t, Options{Run: func(context.Context, mapping.Request) (any, error) {
		t.Error("schema tool must not execute mappings")
		return nil, nil
	}})
	result := decode[schema.Result](t, call(t, client, "bloblang_schema", map[string]any{
		"input": map[string]any{"name": "Ada", "tags": map[string]int{"a": 1, "b": 2}}, "mapPaths": []string{"/tags"},
	}))
	var inferred map[string]any
	if err := json.Unmarshal(result.Schema, &inferred); err != nil {
		t.Fatal(err)
	}
	properties, ok := inferred["properties"].(map[string]any)
	if !ok || properties["tags"].(map[string]any)["values"] == nil {
		t.Fatalf("missing JTD object/map schema: %s", result.Schema)
	}
	if result.Meta.Format != "jtd" || !result.Meta.SampleBased || result.Meta.EnumInference || len(result.Meta.Warnings) == 0 {
		t.Fatalf("missing inference caveats: %+v", result.Meta)
	}
	path := filepath.Join(t.TempDir(), "large.json")
	if err := os.WriteFile(path, []byte(`{"name":"`+strings.Repeat("x", 120_000)+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	fileResult := decode[schema.Result](t, call(t, client, "bloblang_schema", map[string]any{"inputPath": path}))
	if !strings.Contains(string(fileResult.Schema), `"string"`) {
		t.Fatalf("missing large file schema: %s", fileResult.Schema)
	}
	samplePath := filepath.Join(t.TempDir(), "sample.yaml")
	if err := os.WriteFile(samplePath, []byte("$bloblang:\n  input:\n    title: Example\n  meta:\n    topic: ignored\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sampleResult := decode[schema.Result](t, call(t, client, "bloblang_schema", map[string]any{"samplePath": samplePath}))
	if !strings.Contains(string(sampleResult.Schema), "title") || strings.Contains(string(sampleResult.Schema), "topic") {
		t.Fatalf("wrong sample envelope field inferred: %s", sampleResult.Schema)
	}
	for _, arguments := range []map[string]any{
		{}, {"input": nil, "inputPath": path}, {"input": nil, "mapPaths": []string{"invalid"}}, {"input": nil, "enumInference": true},
	} {
		if response := call(t, client, "bloblang_schema", arguments); !response.IsError {
			t.Fatalf("invalid schema arguments accepted: %+v", arguments)
		}
	}
}

func TestSearchBatchAndDocs(t *testing.T) {
	client := connect(t, Options{})
	result := call(t, client, "bloblang_search", help.SearchRequest{Queries: []help.Query{{ID: "typo", Text: "uppercse"}, {ID: "exact", Text: "uppercase"}}, LimitPerQuery: 2})
	response := decode[help.SearchResponse](t, result)
	if !response.Meta.Capabilities.Batch || response.Meta.Capabilities.Semantic || len(response.Meta.Queries) != 2 {
		t.Fatalf("incorrect search capabilities/metadata: %+v", response.Meta)
	}
	var found bool
	for _, result := range response.Results {
		if result.ID == "method:uppercase" {
			found = true
			if len(result.Matches) != 2 || len(result.Matches[0].Evidence) == 0 || result.Matches[0].QueryID != "typo" || result.Matches[1].QueryID != "exact" {
				t.Fatalf("missing per-query evidence: %+v", result)
			}
		}
	}
	if !found {
		t.Fatalf("missing uppercase: %+v", response)
	}
	docs := decode[help.GetResponse](t, call(t, client, "bloblang_docs", map[string]any{"ids": []string{"method:uppercase", "method:nonexistent"}}))
	if len(docs.Results) != 1 || len(docs.Results[0].Examples) == 0 || len(docs.Meta.Queries) != 2 || docs.Meta.Queries[1].Error == nil {
		t.Fatalf("missing docs or per-ID error: %+v", docs)
	}
	if result := call(t, client, "bloblang_search", map[string]any{"queries": []help.Query{{ID: "q", Text: "x"}}, "mode": "semantic"}); !result.IsError {
		t.Fatal("semantic request silently accepted")
	}
	if result := call(t, client, "bloblang_search", help.SearchRequest{Queries: []help.Query{{ID: "q", Text: "x"}, {ID: "q", Text: "y"}}}); !result.IsError {
		t.Fatal("ambiguous duplicate query IDs accepted")
	}
}

func TestStaticToolsAndFiles(t *testing.T) {
	client := connect(t, Options{})
	source := "root=this.name.uppercase()"
	formatted := decode[formatResult](t, call(t, client, "bloblang_format", map[string]any{"source": source}))
	if !formatted.Changed || !strings.Contains(formatted.Source, "root = this.name.uppercase()") {
		t.Fatalf("unexpected formatting: %+v", formatted)
	}
	path := filepath.Join(t.TempDir(), "mapping.blobl")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	fileFormatted := decode[formatResult](t, call(t, client, "bloblang_format", map[string]any{"mappingPath": path}))
	if fileFormatted != formatted {
		t.Fatalf("file and inline results differ: %+v / %+v", fileFormatted, formatted)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != source {
		t.Fatalf("format modified source file: %q, %v", data, err)
	}
	linted := decode[lintResult](t, call(t, client, "bloblang_lint", map[string]any{"source": "root = ("}))
	if len(linted.Diagnostics) == 0 || linted.Diagnostics[0].Line < 1 || linted.Diagnostics[0].Column < 1 {
		t.Fatalf("missing source diagnostics: %+v", linted)
	}
	imported := "import \"common.blobl\"\nroot = this.apply(\"common\")"
	linted = decode[lintResult](t, call(t, client, "bloblang_lint", map[string]any{"source": imported, "imports": map[string]string{"common.blobl": "map common { root = this }"}}))
	if len(linted.Diagnostics) != 0 {
		t.Fatalf("explicit import was not used: %+v", linted)
	}
	linted = decode[lintResult](t, call(t, client, "bloblang_lint", map[string]any{"source": imported}))
	if len(linted.Diagnostics) == 0 {
		t.Fatal("missing explicit import was not diagnosed")
	}
	if result := call(t, client, "bloblang_lint", map[string]any{"mappingPath": filepath.Dir(path)}); !result.IsError {
		t.Fatal("directory accepted as mapping file")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", mapping.MaxRequestBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if result := call(t, client, "bloblang_lint", map[string]any{"mappingPath": path}); !result.IsError {
		t.Fatal("oversized file accepted")
	}
}
