package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These tests cross both process boundaries: the public CLI and its isolated
// mapping worker. Unit tests with an injected runner cannot cover either one.
func TestBinaryMappingCommands(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "blobl.exe")
	build := exec.Command("go", "build", "-o", bin, ".")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))

	t.Run("large numeric IDs survive all fixture channels", func(t *testing.T) {
		result := runBinaryMapping(t, bin, "", "run",
			"--source", "root.input = this.id\nroot.metadata = @id",
			"--input", `{"id":9007199254740993}`,
			"--metadata", `{"id":9007199254740995}`,
			"--root", `{"seed":9007199254740997}`)
		require.Equal(t, 0, result.exitCode, result.stderr)
		var response binaryMappingResult
		decodeBinaryJSON(t, result.stdout, &response)
		require.Nil(t, response.Error)
		require.Nil(t, response.LintError)
		require.Equal(t, map[string]any{
			"input":    json.Number("9007199254740993"),
			"metadata": json.Number("9007199254740995"),
			"seed":     json.Number("9007199254740997"),
		}, response.Output)
		require.Equal(t, map[string]any{"id": json.Number("9007199254740995")}, response.Metadata)
		require.NotNil(t, response.Diagnostics, "empty diagnostics must be an array, not null")
	})

	t.Run("100 KB file input", func(t *testing.T) {
		dir := t.TempDir()
		mapping := writeBinaryFixture(t, dir, "mapping.blobl", "root.id = this.id\nroot.bytes = this.payload.length()")
		input := writeBinaryFixture(t, dir, "input.json", `{"id":9007199254740993,"payload":"`+strings.Repeat("x", 100_000)+`"}`)
		result := runBinaryMapping(t, bin, "", "run", "--input-file", input, mapping)
		require.Equal(t, 0, result.exitCode, result.stderr)
		var response binaryMappingResult
		decodeBinaryJSON(t, result.stdout, &response)
		require.Nil(t, response.Error)
		require.Equal(t, map[string]any{"id": json.Number("9007199254740993"), "bytes": json.Number("100000")}, response.Output)
	})

	t.Run("sample discovery and transitive relative imports", func(t *testing.T) {
		dir := t.TempDir()
		mapping := writeBinaryFixture(t, dir, "mapping.blobl", "import \"lib/helper.blobl\"\nroot = this.apply(\"helper\")\nmeta fixture = env(\"FIXTURE\").or(\"missing\")")
		require.NoError(t, os.Mkdir(filepath.Join(dir, "lib"), 0o700))
		writeBinaryFixture(t, dir, "lib/helper.blobl", "import \"leaf.blobl\"\nmap helper { root = this.apply(\"leaf\") }")
		writeBinaryFixture(t, dir, "lib/leaf.blobl", "map leaf { root = this.value.uppercase() }")
		writeBinaryFixture(t, dir, "mapping.sample.json", `{"$bloblang":{"input":{"value":"hello"},"env":{"FIXTURE":"sample"}}}`)
		result := runBinaryMapping(t, bin, "", "run", mapping)
		require.Equal(t, 0, result.exitCode, result.stderr)
		var response binaryMappingResult
		decodeBinaryJSON(t, result.stdout, &response)
		require.Nil(t, response.Error)
		require.Nil(t, response.LintError)
		require.Empty(t, response.Diagnostics)
		require.Equal(t, "HELLO", response.Output)
		require.Equal(t, map[string]any{"fixture": "sample"}, response.Metadata)
	})

	t.Run("explicit sample file preserves large numeric IDs", func(t *testing.T) {
		dir := t.TempDir()
		sample := writeBinaryFixture(t, dir, "fixture.json", `{"$bloblang":{"input":{"id":9007199254740993},"meta":{"id":9007199254740995},"root":{"seed":9007199254740997}}}`)
		result := runBinaryMapping(t, bin, "", "run", "--sample", sample, "--source", "root.input = this.id\nroot.metadata = @id")
		require.Equal(t, 0, result.exitCode, result.stderr)
		var response binaryMappingResult
		decodeBinaryJSON(t, result.stdout, &response)
		require.Nil(t, response.Error)
		require.Equal(t, map[string]any{
			"input":    json.Number("9007199254740993"),
			"metadata": json.Number("9007199254740995"),
			"seed":     json.Number("9007199254740997"),
		}, response.Output)
		require.Equal(t, map[string]any{"id": json.Number("9007199254740995")}, response.Metadata)
	})

	t.Run("lint accompanies runtime success and failure", func(t *testing.T) {
		for _, tc := range []struct {
			name, expression string
			exitCode         int
		}{
			{name: "success", expression: "this", exitCode: 0},
			{name: "failure", expression: `throw("intentional failure")`, exitCode: 1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				result := runBinaryMapping(t, bin, "", "run", "--source", "let unused = 1\nroot = "+tc.expression, "--input", `{"value":42}`)
				require.Equal(t, tc.exitCode, result.exitCode, result.stderr)
				require.Empty(t, result.stderr)
				var response binaryMappingResult
				decodeBinaryJSON(t, result.stdout, &response)
				require.Nil(t, response.LintError)
				require.NotEmpty(t, response.Diagnostics)
				var unused *binaryDiagnostic
				for i := range response.Diagnostics {
					if response.Diagnostics[i].Code == "correctness/variables/no-unused-let" {
						unused = &response.Diagnostics[i]
					}
				}
				require.NotNil(t, unused)
				require.Equal(t, 1, unused.Line)
				require.Positive(t, unused.Column)
				require.Equal(t, 1, unused.EndLine)
				require.Greater(t, unused.EndColumn, unused.Column)
				require.NotEmpty(t, unused.Severity)
				require.NotEmpty(t, unused.Message)
				if tc.exitCode == 0 {
					require.Nil(t, response.Error)
					require.Equal(t, map[string]any{"value": json.Number("42")}, response.Output)
				} else {
					require.NotNil(t, response.Error)
					require.Equal(t, "execute", response.Error.Kind)
					require.Contains(t, response.Error.Message, "intentional failure")
					require.Nil(t, response.Output)
				}
			})
		}
	})

	t.Run("fixture suite uses worker and preserves exact assertions", func(t *testing.T) {
		suite := `source: root = this
cases:
  - name: large integer
    request: {input: {id: 9007199254740993}}
    expect: {output: {id: 9007199254740993}}
  - name: expected runtime error
    request: {source: 'root = throw("expected")', input: null}
    expect: {error: {kind: execute, contains: expected}}
  - name: exact integer mismatch
    request: {input: {id: 9007199254740993}}
    expect: {output: {id: 9007199254740992}}
`
		result := runBinaryMapping(t, bin, suite, "test", "--format", "json", "-")
		require.Equal(t, 1, result.exitCode, result.stderr)
		require.Empty(t, result.stderr)
		var response struct {
			Passed, Failed int
			Cases          []struct {
				Name     string
				Passed   bool
				Result   binaryMappingResult
				Failures []string
			}
		}
		decodeBinaryJSON(t, result.stdout, &response)
		require.Equal(t, 2, response.Passed)
		require.Equal(t, 1, response.Failed)
		require.Len(t, response.Cases, 3)
		require.True(t, response.Cases[0].Passed)
		require.Equal(t, map[string]any{"id": json.Number("9007199254740993")}, response.Cases[0].Result.Output)
		require.NotNil(t, response.Cases[0].Result.Diagnostics)
		require.True(t, response.Cases[1].Passed)
		require.Equal(t, "execute", response.Cases[1].Result.Error.Kind)
		require.False(t, response.Cases[2].Passed)
		require.Contains(t, strings.Join(response.Cases[2].Failures, "\n"), "9007199254740992")
	})

	t.Run("lint configuration failure stays structured", func(t *testing.T) {
		dir := t.TempDir()
		mapping := writeBinaryFixture(t, dir, "mapping.blobl", "root = this")
		writeBinaryFixture(t, dir, ".bloblangrc.json", "{invalid")
		result := runBinaryMapping(t, bin, "", "run", "--input", `{"value":42}`, mapping)
		require.Equal(t, 1, result.exitCode, result.stderr)
		require.Empty(t, result.stderr)
		var response binaryMappingResult
		decodeBinaryJSON(t, result.stdout, &response)
		require.Nil(t, response.Error)
		require.NotNil(t, response.LintError)
		require.Equal(t, "config", response.LintError.Kind)
		require.NotNil(t, response.Diagnostics)
		require.Equal(t, map[string]any{"value": json.Number("42")}, response.Output)
	})

	t.Run("passing suite from file", func(t *testing.T) {
		suite := writeBinaryFixture(t, t.TempDir(), "suite.yaml", `source: root = this.uppercase()
cases:
  - name: uppercase
    request: {input: hello}
    expect: {output: HELLO}
`)
		result := runBinaryMapping(t, bin, "", "test", suite)
		require.Equal(t, 0, result.exitCode, result.stderr)
		require.Empty(t, result.stderr)
		require.Contains(t, result.stdout, "PASS uppercase\n")
		require.Contains(t, result.stdout, "1 passed, 0 failed\n")
	})

	t.Run("hidden worker protocol", func(t *testing.T) {
		for _, tc := range []struct {
			name, request, errorKind string
		}{
			{name: "success", request: `{"source":"root = this","input":{"id":9007199254740993},"metadata":{"id":9007199254740995}}`},
			{name: "missing input", request: `{"source":"root = this"}`, errorKind: "input"},
			{name: "unknown field", request: `{"source":"root = this","input":null,"unexpected":true}`, errorKind: "input"},
			{name: "trailing request", request: `{"source":"root = this","input":null} {}`, errorKind: "input"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				result := runBinaryMapping(t, bin, tc.request, "__mapping-worker")
				require.Equal(t, 0, result.exitCode, result.stderr)
				require.Empty(t, result.stderr)
				var response binaryMappingResult
				decodeBinaryJSON(t, result.stdout, &response)
				if tc.errorKind != "" {
					require.NotNil(t, response.Error)
					require.Equal(t, tc.errorKind, response.Error.Kind)
					return
				}
				require.Nil(t, response.Error)
				require.Equal(t, map[string]any{"id": json.Number("9007199254740993")}, response.Output)
				require.Equal(t, map[string]any{"id": json.Number("9007199254740995")}, response.Metadata)
			})
		}
	})

	t.Run("hard execution timeout", func(t *testing.T) {
		// The nested folds spend CPU without constructing a huge result or
		// requiring unbounded memory. A small input first proves validity.
		source := "let n = this.n\nroot = range(0, $n).fold(0, a -> a.tally + range(0, $n).fold(0, b -> b.tally + range(0, $n).fold(0, c -> c.tally + 1)))"
		result := runBinaryMapping(t, bin, "", "run", "--source", source, "--input", `{"n":2}`)
		require.Equal(t, 0, result.exitCode, result.stderr+result.stdout)
		var small binaryMappingResult
		decodeBinaryJSON(t, result.stdout, &small)
		require.Equal(t, json.Number("8"), small.Output)
		started := time.Now()
		result = runBinaryMapping(t, bin, "", "run", "--timeout", "100ms", "--source", source, "--input", `{"n":1000}`)
		require.Less(t, time.Since(started), 3*time.Second)
		require.Equal(t, 1, result.exitCode, result.stderr+result.stdout)
		var response binaryMappingResult
		decodeBinaryJSON(t, result.stdout, &response)
		require.NotNil(t, response.Error)
		require.Equal(t, "timeout", response.Error.Kind)
		require.NotNil(t, response.Diagnostics)
		require.Nil(t, response.Output)
	})
}

type binaryDiagnostic struct {
	Line, Column, EndLine, EndColumn int
	Severity, Code, Message          string
}

type binaryMappingError struct {
	Kind, Message string
}

type binaryMappingResult struct {
	Output           any
	Metadata         map[string]any
	Diagnostics      []binaryDiagnostic
	Error, LintError *binaryMappingError
}

type binaryCommandResult struct {
	stdout, stderr string
	exitCode       int
}

func runBinaryMapping(t *testing.T, bin, stdin string, args ...string) binaryCommandResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	// Inline mappings must not inherit the repository's configuration.
	cmd.Dir = t.TempDir()
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	require.NoError(t, ctx.Err(), "command did not terminate: %v; stdout=%s; stderr=%s", args, stdout.String(), stderr.String())
	result := binaryCommandResult{stdout: stdout.String(), stderr: stderr.String()}
	if err != nil {
		var exit *exec.ExitError
		require.True(t, errors.As(err, &exit), "could not run command: %v", err)
		result.exitCode = exit.ExitCode()
	}
	return result
}

func decodeBinaryJSON(t *testing.T, data string, target any) {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(target), data)
	// Reject banners, logs or a second protocol response on stdout.
	require.ErrorIs(t, decoder.Decode(new(any)), io.EOF, data)
}

func writeBinaryFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}
