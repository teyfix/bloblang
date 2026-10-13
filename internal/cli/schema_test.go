package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/teyfix/bloblang-lsp/internal/schema"
	"go.yaml.in/yaml/v3"
)

func invokeSchema(input string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := runSchemaCommand(context.Background(), args, strings.NewReader(input), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestSchemaCommandFileAndEnvelope(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.yaml")
	sample := filepath.Join(dir, "mapping.sample.json")
	writeInput(t, input, "regions:\n  US:\n    name: A\n  TR:\n    name: B\n")
	writeInput(t, sample, `{"$bloblang":{"input":{"count":1},"root":{"different":true},"meta":{"unused":"x"}}}`)
	code, stdout, stderr := invokeSchema("", "--input-file", input, "--map-path", "/regions")
	require.Equal(t, 0, code, stderr)
	var result schema.Result
	require.NoError(t, json.Unmarshal([]byte(stdout), &result))
	require.JSONEq(t, `{"properties":{"regions":{"values":{"properties":{"name":{"type":"string"}}}}}}`, string(result.Schema))
	code, stdout, stderr = invokeSchema("", "--sample", sample, "--format", "yaml")
	require.Equal(t, 0, code, stderr)
	var document map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(stdout), &document))
	encoded, err := json.Marshal(document["schema"])
	require.NoError(t, err)
	require.JSONEq(t, `{"properties":{"count":{"type":"float64"}}}`, string(encoded))
	require.Equal(t, false, document["meta"].(map[string]any)["enumInference"])
}

func TestSchemaCommandStdinAndFailures(t *testing.T) {
	code, stdout, stderr := invokeSchema("- a\n- null\n")
	require.Equal(t, 0, code, stderr)
	var result schema.Result
	require.NoError(t, json.Unmarshal([]byte(stdout), &result))
	require.JSONEq(t, `{"elements":{"nullable":true,"type":"string"}}`, string(result.Schema))
	code, stdout, stderr = invokeSchema("", "--help")
	require.Equal(t, 0, code, stderr)
	require.Contains(t, stdout, "JSON Type Definition")
	for _, args := range [][]string{
		{"--input-file", "a", "--sample", "b"}, {"--format", "typescript"}, {"--map-path", "/bad~2"}, {"unexpected"},
	} {
		code, _, stderr = invokeSchema("null", args...)
		require.Equal(t, 2, code, args)
		require.NotEmpty(t, stderr)
	}
	for _, input := range []string{"", "{}\n---\n{}", "[[],1,\"x\"]", strings.Repeat("x", schema.MaxBytes+1)} {
		code, _, stderr = invokeSchema(input)
		require.Equal(t, 2, code)
		require.NotEmpty(t, stderr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code = runSchemaCommand(ctx, nil, strings.NewReader("null"), &bytes.Buffer{}, &bytes.Buffer{})
	require.Equal(t, 2, code)
	code = runSchemaCommand(context.Background(), nil, strings.NewReader("null"), brokenWriter{}, &bytes.Buffer{})
	require.Equal(t, 2, code)
}
