package benthos

import (
	"github.com/stretchr/testify/require"
	"github.com/teyfix/bloblang/internal/fileuri"
	"os"
	"path/filepath"
	"testing"
)

func absentEnv(t *testing.T, name string) {
	t.Helper()
	value, exists := os.LookupEnv(name)
	require.NoError(t, os.Unsetenv(name))
	t.Cleanup(func() {
		if exists {
			_ = os.Setenv(name, value)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}
func TestPreviewEnvDoesNotChangeRuntime(t *testing.T) {
	name := "BLOBLANG_LSP_TEST_MISSING_ENV"
	absentEnv(t, name)
	base := NewEnvironment()
	parser := newTestParser(t)
	executor := NewExecutor(base, testExecutorConfig())
	text := `root = ["email=" + env("` + name + `"), "ref=" + this.provider_file_ref]`
	sample := &Sample{Value: map[string]any{"provider_file_ref": "file-ref"}}
	result, err := executor.ExecuteThrough(parser, "file:///mapping.blobl", sample, text, uint(len(text)))
	require.NoError(t, err)
	require.Equal(t, []any{"email=", "ref=file-ref"}, result.Value)
	actual, err := base.Parse(`root = env("` + name + `")`)
	require.NoError(t, err)
	value, err := actual.Query(nil)
	require.NoError(t, err)
	require.Nil(t, value, "private override must not change the shared environment")
	_, err = base.Parse(text)
	require.Error(t, err, "real parser constant-folding string + null retains its failure")
	_, exists := os.LookupEnv(name)
	require.False(t, exists, "preview must not mutate process environment")
	t.Setenv(name, "process-value")
	result, err = executor.ExecuteThrough(parser, "file:///mapping.blobl", sample, text, uint(len(text)))
	require.NoError(t, err)
	require.Equal(t, []any{"email=", "ref=file-ref"}, result.Value, "preview must not read real process env values")
	actual, err = base.Parse(`root = env("` + name + `")`)
	require.NoError(t, err)
	value, err = actual.Query(nil)
	require.NoError(t, err)
	require.Equal(t, "process-value", value)
	sample.Env = map[string]string{name: "sample-value"}
	result, err = executor.ExecuteThrough(parser, "file:///mapping.blobl", sample, text, uint(len(text)))
	require.NoError(t, err)
	require.Equal(t, []any{"email=sample-value", "ref=file-ref"}, result.Value)
	sample.Env[name] = ""
	result, err = executor.ExecuteThrough(parser, "file:///mapping.blobl", sample, text, uint(len(text)))
	require.NoError(t, err)
	require.Equal(t, []any{"email=", "ref=file-ref"}, result.Value)
}
func TestPreviewEnvironmentInImportedMapAndDynamicLookup(t *testing.T) {
	name := "BLOBLANG_LSP_TEST_IMPORTED_ENV"
	absentEnv(t, name)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "import.blobl"), []byte(`map lookup { root = "import=" + env(this.name) }`), 0600))
	text := "import \"import.blobl\"\nroot = this.apply(\"lookup\")"
	executor := NewExecutor(NewEnvironment(), testExecutorConfig())
	result, err := executor.ExecuteThrough(newTestParser(t), fileuri.FromPath(filepath.Join(dir, "mapping.blobl")), &Sample{Value: map[string]any{"name": name}}, text, uint(len(text)))
	require.NoError(t, err)
	require.Equal(t, "import=", result.Value)
}
func TestSampleEnvOverridesValidation(t *testing.T) {
	parser := newTestParser(t)
	sample, err := ExtractSample(parser, "file:///mapping.blobl", `#!sample {"input":{},"env":{"EXAMPLE":"fixture-value","EMPTY":""}}`, t.TempDir())
	require.NoError(t, err)
	require.Equal(t, map[string]string{"EXAMPLE": "fixture-value", "EMPTY": ""}, sample.Env)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mapping.sample.yaml"), []byte("$bloblang:\n  input: {}\n  env:\n    EXAMPLE: file-fixture\n"), 0600))
	sample, err = ExtractSample(parser, "file:///mapping.blobl", "root = this", dir)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"EXAMPLE": "file-fixture"}, sample.Env)
	for _, env := range []string{`null`, `[]`, `{"EXAMPLE":1}`} {
		_, err := ExtractSample(parser, "file:///mapping.blobl", `#!sample {"input":{},"env":`+env+`}`, t.TempDir())
		require.Error(t, err)
	}
}
