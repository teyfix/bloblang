package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teyfix/bloblang-lsp/internal/mapping"
)

func useDirectMappingRunner(t *testing.T) {
	t.Helper()
	previous := executeMapping
	executeMapping = func(_ context.Context, req mapping.Request) mapping.Result { return mapping.Run(req) }
	t.Cleanup(func() { executeMapping = previous })
}

func TestRunMappingCommand(t *testing.T) {
	useDirectMappingRunner(t)
	var out, errOut bytes.Buffer
	exit := runMappingCommand(context.Background(), []string{
		"--source", "root = this.value.uppercase()\nmeta source = env(\"SOURCE\")",
		"--input", `{"value":"hello"}`,
		"--metadata", `{"keep":"yes"}`, "--env", `{"SOURCE":"fixture"}`,
	}, strings.NewReader(""), &out, &errOut)
	require.Equal(t, 0, exit, errOut.String())
	var result mapping.Result
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	assert.Equal(t, "HELLO", result.Output)
	assert.Equal(t, map[string]any{"keep": "yes", "source": "fixture"}, result.Metadata)
	assert.NotNil(t, result.RawOutput)
}

func TestRunMappingCommandFilesAndStdin(t *testing.T) {
	useDirectMappingRunner(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "main.blobl")
	require.NoError(t, os.WriteFile(path, []byte(`root = this.value`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.sample.yaml"), []byte("$bloblang:\n  input:\n    value: sample\n"), 0o600))
	inputPath := filepath.Join(dir, "input.yaml")
	require.NoError(t, os.WriteFile(inputPath, []byte("value: file\n"), 0o600))
	for _, tt := range []struct {
		name, stdin, want string
		args              []string
	}{
		{"sibling sample", "", "sample", []string{path}},
		{"input file", "", "file", []string{"--input-file", inputPath, path}},
		{"input stdin", `{"value":"stdin"}`, "stdin", []string{"--input-file", "-", path}},
		{"source stdin", `root = this.value`, "inline", []string{"--input", `{"value":"inline"}`, "-"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			exit := runMappingCommand(context.Background(), tt.args, strings.NewReader(tt.stdin), &out, &errOut)
			require.Equal(t, 0, exit, errOut.String())
			var result mapping.Result
			require.NoError(t, json.Unmarshal(out.Bytes(), &result))
			assert.Equal(t, tt.want, result.Output)
		})
	}
}

func TestRunMappingCommandImports(t *testing.T) {
	useDirectMappingRunner(t)
	path := filepath.Join(t.TempDir(), "helper.blobl")
	require.NoError(t, os.WriteFile(path, []byte(`map helper { root = this.uppercase() }`), 0o600))
	var out, errOut bytes.Buffer
	exit := runMappingCommand(context.Background(), []string{
		"--source", "import \"helper.blobl\"\nroot = this.apply(\"helper\")",
		"--input", `"hello"`, "--import", "helper.blobl=" + path,
	}, strings.NewReader(""), &out, &errOut)
	require.Equal(t, 0, exit, errOut.String())
	var result mapping.Result
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	assert.Equal(t, "HELLO", result.Output)
}

func TestRunMappingCommandInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--source", "root = this"},
		{"--source", "root = this", "mapping.blobl"},
		{"--source", "root = this", "--input", ""},
		{"--source", "root = this", "--input", "{} {}"},
		{"--source", "root = this", "--input", "null", "--root", "invalid"},
		{"--source", "root = this", "--input", "null", "--metadata", "null"},
		{"--source", "root = this", "--input", "null", "--env", `{"X":2}`},
		{"--source", "root = this", "--input", "null", "--timeout", "0s"},
		{"--source", "root = this", "--input", "null", "--timeout", "1m"},
		{"--input-file", "-", "-"},
		{"--input", "null", "--input-file", "-", "--source", "root = this"},
		{"--import", "malformed", "--source", "root = this", "--input", "null"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			assert.Equal(t, 2, runMappingCommand(context.Background(), args, strings.NewReader(""), &out, &errOut))
			assert.NotEmpty(t, errOut.String())
			assert.Empty(t, out.String())
		})
	}
}

func TestRunMappingCommandErrorIsStructured(t *testing.T) {
	useDirectMappingRunner(t)
	var out, errOut bytes.Buffer
	exit := runMappingCommand(context.Background(), []string{"--source", `root = throw("boom")`, "--input", "null"}, strings.NewReader(""), &out, &errOut)
	require.Equal(t, 1, exit)
	assert.Empty(t, errOut.String())
	var result mapping.Result
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	require.NotNil(t, result.Error)
	assert.Equal(t, "execute", result.Error.Kind)
}

func TestRunMappingCommandLintSetupErrorFails(t *testing.T) {
	useDirectMappingRunner(t)
	executeMapping = func(context.Context, mapping.Request) mapping.Result {
		return mapping.Result{Output: "valid", LintError: &mapping.Error{Kind: "config", Message: "invalid configuration"}}
	}
	var out, errOut bytes.Buffer
	exit := runMappingCommand(context.Background(), []string{"--source", `root = "valid"`, "--input", "null"}, strings.NewReader(""), &out, &errOut)
	require.Equal(t, 1, exit)
	assert.Empty(t, errOut.String())
	var result mapping.Result
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	assert.Equal(t, "valid", result.Output)
	require.NotNil(t, result.LintError)
	assert.Equal(t, "config", result.LintError.Kind)
}

func TestRunFixtureCommand(t *testing.T) {
	useDirectMappingRunner(t)
	suite := `source: root = this.uppercase()
cases:
  - name: uppercase
    request: {input: hello}
    expect: {output: HELLO}
  - name: wrong expectation
    request: {input: goodbye}
    expect: {output: nope}
`
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var out, errOut bytes.Buffer
			exit := runTestCommand(context.Background(), []string{"--format", format, "-"}, strings.NewReader(suite), &out, &errOut)
			require.Equal(t, 1, exit, errOut.String())
			if format == "json" {
				var result mapping.SuiteResult
				require.NoError(t, json.Unmarshal(out.Bytes(), &result))
				assert.Equal(t, 1, result.Passed)
				assert.Equal(t, 1, result.Failed)
			} else {
				assert.Contains(t, out.String(), "PASS uppercase\nFAIL wrong expectation\n")
				assert.Contains(t, out.String(), "1 passed, 1 failed")
			}
		})
	}
}

func TestMappingCommandHelp(t *testing.T) {
	for _, command := range []func(context.Context, []string, io.Reader, io.Writer, io.Writer) int{runMappingCommand, runTestCommand} {
		var out, errOut bytes.Buffer
		assert.Zero(t, command(context.Background(), []string{"--help"}, strings.NewReader(""), &out, &errOut))
		assert.Contains(t, out.String(), "Usage: blobl")
		assert.Empty(t, errOut.String())
	}
}
