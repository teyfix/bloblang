package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/teyfix/bloblang/internal/help"
)

func TestBinaryMCPStdio(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "blobl.exe")
	build := exec.Command("go", "build", "-o", bin, ".")
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, bin, "mcp")
	cmd.Dir = t.TempDir()
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = stdin.Close()
		err := cmd.Wait()
		require.NoError(t, ctx.Err(), "MCP server did not terminate after stdin closed")
		require.NoError(t, err, stderr.String())
		require.Empty(t, stderr.String())
	})
	encoder := json.NewEncoder(stdin)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	nextID := 0
	request := func(method string, params any, target any) {
		t.Helper()
		nextID++
		require.NoError(t, encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": nextID, "method": method, "params": params}))
		for {
			require.True(t, scanner.Scan(), "MCP response unavailable: %v", scanner.Err())
			var response struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
				Method  string          `json:"method"`
				Result  json.RawMessage `json:"result"`
				Error   json.RawMessage `json:"error"`
			}
			decodeBinaryJSON(t, scanner.Text(), &response)
			require.Equal(t, "2.0", response.JSONRPC)
			if len(response.ID) == 0 && response.Method != "" {
				continue // Valid server notifications may precede a response.
			}
			require.Equal(t, fmt.Sprint(nextID), string(response.ID))
			require.Empty(t, response.Error, "unexpected protocol error: %s", response.Error)
			decodeBinaryJSON(t, string(response.Result), target)
			return
		}
	}
	var initialized struct {
		ProtocolVersion string
		ServerInfo      struct{ Name, Version string }
		Instructions    string
	}
	request("initialize", map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "blobl-binary-test", "version": "1"},
	}, &initialized)
	require.Equal(t, "2025-11-25", initialized.ProtocolVersion)
	require.Equal(t, "bloblang", initialized.ServerInfo.Name)
	require.Equal(t, "dev", initialized.ServerInfo.Version)
	require.Contains(t, initialized.Instructions, "fuzzy")
	require.NoError(t, encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}))

	var listed struct {
		Tools []struct {
			Name, Description string
			Annotations       struct{ ReadOnlyHint bool }
		}
	}
	request("tools/list", map[string]any{}, &listed)
	found := map[string]bool{}
	for _, tool := range listed.Tools {
		found[tool.Name] = true
		require.True(t, tool.Annotations.ReadOnlyHint, tool.Name)
		if tool.Name == "bloblang_search" {
			require.Contains(t, tool.Description, "batched")
			require.Contains(t, tool.Description, "semantic search is not supported")
		}
	}
	for _, name := range []string{"bloblang_run", "bloblang_lint", "bloblang_format", "bloblang_search", "bloblang_docs"} {
		require.True(t, found[name], "missing %s", name)
	}
	call := func(name string, arguments any) binaryMCPResult {
		t.Helper()
		var result binaryMCPResult
		request("tools/call", map[string]any{"name": name, "arguments": arguments}, &result)
		require.NotEmpty(t, result.StructuredContent, name)
		require.Len(t, result.Content, 1, "JSON text fallback must accompany structured content")
		require.Equal(t, "text", result.Content[0].Type)
		var structured, fallback any
		decodeBinaryJSON(t, string(result.StructuredContent), &structured)
		decodeBinaryJSON(t, result.Content[0].Text, &fallback)
		require.Equal(t, structured, fallback, "structured and fallback results differ")
		return result
	}

	result := call("bloblang_run", json.RawMessage(`{"source":"let unused = 1\nroot.input = this.id\nroot.metadata = @id","input":{"id":9007199254740993},"metadata":{"id":9007199254740995},"root":{"seed":9007199254740997}}`))
	require.False(t, result.IsError)
	var evaluated binaryMappingResult
	decodeBinaryJSON(t, string(result.StructuredContent), &evaluated)
	require.Nil(t, evaluated.Error)
	require.Equal(t, map[string]any{
		"input":    json.Number("9007199254740993"),
		"metadata": json.Number("9007199254740995"),
		"seed":     json.Number("9007199254740997"),
	}, evaluated.Output)
	require.Equal(t, map[string]any{"id": json.Number("9007199254740995")}, evaluated.Metadata)
	require.NotEmpty(t, evaluated.Diagnostics)

	result = call("bloblang_lint", map[string]any{"source": "let unused = 1\nroot = this"})
	require.False(t, result.IsError)
	var linted struct{ Diagnostics []binaryDiagnostic }
	decodeBinaryJSON(t, string(result.StructuredContent), &linted)
	require.NotEmpty(t, linted.Diagnostics)
	require.Equal(t, "correctness/variables/no-unused-let", linted.Diagnostics[0].Code)

	result = call("bloblang_run", map[string]any{"source": `root = throw("intentional failure")`, "input": nil})
	require.True(t, result.IsError)
	var failed binaryMappingResult
	decodeBinaryJSON(t, string(result.StructuredContent), &failed)
	require.NotNil(t, failed.Error)
	require.Equal(t, "execute", failed.Error.Kind)
	require.Contains(t, failed.Error.Message, "intentional failure")
	require.NotNil(t, failed.Diagnostics)

	started := time.Now()
	result = call("bloblang_run", map[string]any{
		"source": "let n = this.n\nroot = range(0, $n).fold(0, a -> a.tally + range(0, $n).fold(0, b -> b.tally + range(0, $n).fold(0, c -> c.tally + 1)))",
		"input":  map[string]any{"n": 1000}, "timeoutMs": 100,
	})
	require.Less(t, time.Since(started), 3*time.Second)
	require.True(t, result.IsError)
	var timeout binaryMappingResult
	decodeBinaryJSON(t, string(result.StructuredContent), &timeout)
	require.NotNil(t, timeout.Error)
	require.Equal(t, "timeout", timeout.Error.Kind)

	// A timed-out worker must leave the persistent MCP server usable.
	result = call("bloblang_search", map[string]any{
		"queries":       []map[string]any{{"id": "typo", "text": "uppercse"}, {"id": "exact", "text": "uppercase"}},
		"limitPerQuery": 2,
	})
	require.False(t, result.IsError)
	var searched help.SearchResponse
	decodeBinaryJSON(t, string(result.StructuredContent), &searched)
	require.True(t, searched.Meta.Capabilities.Batch)
	require.True(t, searched.Meta.Capabilities.FuzzyNames)
	require.True(t, searched.Meta.Capabilities.Lexical)
	require.False(t, searched.Meta.Capabilities.Semantic)
	require.Len(t, searched.Meta.Queries, 2)
	require.Equal(t, "typo", searched.Meta.Queries[0].ID)
	require.Equal(t, "exact", searched.Meta.Queries[1].ID)
	var uppercase *help.SearchResult
	for i := range searched.Results {
		if searched.Results[i].ID == "method:uppercase" {
			require.Nil(t, uppercase, "batched results must be deduplicated")
			uppercase = &searched.Results[i]
		}
	}
	require.NotNil(t, uppercase)
	require.Len(t, uppercase.Matches, 2)
	for i, id := range []string{"typo", "exact"} {
		require.Equal(t, id, uppercase.Matches[i].QueryID)
		require.Positive(t, uppercase.Matches[i].Rank)
		require.NotEmpty(t, uppercase.Matches[i].Evidence)
		require.NotEmpty(t, uppercase.Matches[i].Evidence[0].Field)
		require.NotEmpty(t, uppercase.Matches[i].Evidence[0].Excerpt)
	}
}

type binaryMCPResult struct {
	IsError           bool
	StructuredContent json.RawMessage
	Content           []struct{ Type, Text string }
}
