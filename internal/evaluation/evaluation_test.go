package evaluation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/teyfix/bloblang-lsp/internal/mapping"
)

func direct(_ context.Context, req mapping.Request) mapping.Result { return mapping.Run(req) }

func TestEvaluationReportsLintAlongsideSuccessAndFailure(t *testing.T) {
	for _, source := range []string{"let unused = 1\nroot = this", "let unused = 1\nroot = throw(\"failed\")"} {
		result := run(context.Background(), mapping.Request{Source: source, Input: json.RawMessage(`{"id":1}`)}, direct)
		require.NotEmpty(t, result.Diagnostics)
		require.Nil(t, result.LintError)
		require.Equal(t, "correctness/variables/no-unused-let", result.Diagnostics[0].Code)
		require.Greater(t, result.Diagnostics[0].EndColumn, result.Diagnostics[0].Column)
		if source[len(source)-1] == ')' {
			require.NotNil(t, result.Error)
		} else {
			require.Nil(t, result.Error)
		}
	}
}

func TestEvaluationPreservesSourceConfigAndImports(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mapping.blobl")
	require.NoError(t, os.WriteFile(path, []byte("import \"lib.blobl\"\nlet unused = 1\nroot = this.apply(\"value\")"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lib.blobl"), []byte("map value { root = this.id }"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".bloblangrc.json"), []byte(`{"lint":{"rules":{"correctness/variables/no-unused-let":"off"}}}`), 0600))
	result := run(context.Background(), mapping.Request{MappingPath: path, Input: json.RawMessage(`{"id":123}`)}, direct)
	require.Nil(t, result.Error)
	require.Nil(t, result.LintError)
	require.Empty(t, result.Diagnostics)
	require.Equal(t, json.Number("123"), result.Output)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".bloblangrc.json"), []byte(`{invalid`), 0600))
	result = run(context.Background(), mapping.Request{MappingPath: path, Input: json.RawMessage(`{"id":123}`)}, direct)
	require.Nil(t, result.Error)
	require.NotNil(t, result.LintError)
	require.Equal(t, "config", result.LintError.Kind)
}

func TestEvaluationCanceledDoesNotExecute(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := run(ctx, mapping.Request{Source: "root = this", Input: json.RawMessage(`null`)}, func(context.Context, mapping.Request) mapping.Result {
		t.Fatal("executed canceled request")
		return mapping.Result{}
	})
	require.Equal(t, "canceled", result.Error.Kind)
	require.NotNil(t, result.Diagnostics)
}
