package lsp

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/redpanda-data/benthos/v4/public/bloblang"
	"github.com/stretchr/testify/require"
	"github.com/teyfix/bloblang/internal/editorconfig"
)

func testTooling(t *testing.T) (*Tooling, string) {
	t.Helper()
	tooling, err := NewTooling()
	require.NoError(t, err)
	t.Cleanup(tooling.Close)
	return tooling, t.TempDir()
}

func TestToolingFormatPlainAndYAML(t *testing.T) {
	tooling, dir := testTooling(t)
	for _, tc := range []struct{ name, input, want string }{
		{"mapping.blobl", "root={a:1,b:[2,3,]}\n", "root = { a: 1, b: [2, 3] }\n"},
		{"pipeline.yaml", "# host\nmapping: |-\n  root={a:1}\nnext: preserved # comment\n", "# host\nmapping: |-\n  root = { a: 1 }\nnext: preserved # comment\n"},
		{"pipeline.yml", "check: '!errored() && this.ok'\nnext: preserved\n", "check: '!errored() && this.ok'\nnext: preserved\n"},
		{"comments.blobl", "# empty mapping\n", "# empty mapping\n"},
		{"empty.blobl", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name)
			formatted, err := tooling.Format(path, tc.input, 2)
			require.NoError(t, err)
			require.Equal(t, tc.want, formatted)
			again, err := tooling.Format(path, formatted, 2)
			require.NoError(t, err)
			require.Equal(t, formatted, again)
		})
	}
}

func TestToolingRejectsInvalidFormatAndReportsStaticErrors(t *testing.T) {
	tooling, dir := testTooling(t)
	for _, tc := range []struct{ name, text string }{
		{"syntax.blobl", "root = [\n"},
		{"semantic.blobl", "root = unknown_function()\n"},
		{"semantic.yaml", "mapping: |-\n  root = unknown_function()\n"},
		{"broken.yaml", "mapping: [\n"},
		{"multi.yaml", "mapping: root = this\n---\nmapping: root = [\n"},
		{"multi-valid.yaml", "mapping: root = this\n---\nmapping: root = this\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name)
			_, err := tooling.Format(path, tc.text, 2)
			require.Error(t, err)
			result, err := tooling.Lint(path, tc.text, true)
			require.NoError(t, err)
			require.Equal(t, tc.text, result.Text)
			require.NotEmpty(t, result.Diagnostics)
			require.Equal(t, "error", result.Diagnostics[0].Severity)
			if tc.name == "semantic.yaml" {
				require.Equal(t, 2, result.Diagnostics[0].Line)
				require.GreaterOrEqual(t, result.Diagnostics[0].Column, 3)
			}
		})
	}
}

func TestToolingNearestConfigAndImports(t *testing.T) {
	tooling, dir := testTooling(t)
	child := filepath.Join(dir, "child")
	require.NoError(t, os.Mkdir(child, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".bloblangrc.json"), []byte(`{"lint":{"rules":{"correctness/variables/no-unused-let":"error"}},"formatter":{"printWidth":20}}`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(child, "shared.blobl"), []byte("map identity { root = this }\n"), 0600))
	for _, name := range []string{"mapping.blobl", "mapping.yaml"} {
		text := "import \"shared.blobl\"\nlet unused = 1\nroot = this.apply(\"identity\")\n"
		if strings.HasSuffix(name, ".yaml") {
			text = "mapping: |-\n  " + strings.ReplaceAll(strings.TrimSuffix(text, "\n"), "\n", "\n  ") + "\n"
		}
		result, err := tooling.Lint(filepath.Join(child, name), text, false)
		require.NoError(t, err)
		require.Len(t, result.Diagnostics, 1)
		require.Equal(t, "correctness/variables/no-unused-let", result.Diagnostics[0].Code)
		require.Equal(t, "error", result.Diagnostics[0].Severity)
		_, err = tooling.Format(filepath.Join(child, name), text, 2)
		require.NoError(t, err)
	}
	absoluteImport := fmt.Sprintf("import %q\nroot = this.apply(\"identity\")\n", filepath.Join(child, "shared.blobl"))
	for _, name := range []string{"absolute.blobl", "absolute.yaml"} {
		input := absoluteImport
		if strings.HasSuffix(name, ".yaml") {
			input = "mapping: |-\n  " + strings.ReplaceAll(strings.TrimSuffix(input, "\n"), "\n", "\n  ") + "\n"
		}
		result, err := tooling.Lint(filepath.Join(child, name), input, false)
		require.NoError(t, err)
		require.Empty(t, result.Diagnostics)
		_, err = tooling.Format(filepath.Join(child, name), input, 2)
		require.NoError(t, err)
	}
	text := "root = {foo: this.foo, bar: this.bar, baz: this.baz}\n"
	formatted, err := tooling.Format(filepath.Join(child, "width.blobl"), text, 2)
	require.NoError(t, err)
	require.Contains(t, formatted, "\n  foo:")
	require.NoError(t, os.WriteFile(filepath.Join(child, ".bloblangrc.json"), []byte(`{"lint":{"enabled":false}}`), 0600))
	result, err := tooling.Lint(filepath.Join(child, "mapping.blobl"), "let unused = 1\nroot = this\n", false)
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)
	require.NoError(t, os.WriteFile(filepath.Join(child, ".bloblangrc.json"), []byte(`{"unknown":true}`), 0600))
	_, err = tooling.Lint(filepath.Join(child, "mapping.blobl"), "root = this", false)
	require.ErrorContains(t, err, ".bloblangrc.json")
	_, err = tooling.Format(filepath.Join(child, "mapping.blobl"), "root = this", 2)
	require.ErrorContains(t, err, ".bloblangrc.json")
}

func TestToolingSafeFixesConvergeAndPreserveHost(t *testing.T) {
	tooling, dir := testTooling(t)
	for _, tc := range []struct{ name, text, want string }{
		{"mapping.blobl", `root = this.without("a").without("b").without("c").without("d")` + "\n", `root = this.without("a", "b", "c", "d")` + "\n"},
		{"mapping.yaml", "# host\nmapping: |-\n  root = this.without(\"a\").without(\"b\")\nnext: preserved\n", "# host\nmapping: |-\n  root = this.without(\"a\", \"b\")\nnext: preserved\n"},
		{"quoted.yaml", "mapping: 'root = this.without(\"a\").without(\"b\")'\n", "mapping: 'root = this.without(\"a\").without(\"b\")'\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name)
			original, err := tooling.Lint(path, tc.text, false)
			require.NoError(t, err)
			require.NotEmpty(t, original.Diagnostics)
			fixed, err := tooling.Lint(path, tc.text, true)
			require.NoError(t, err)
			require.Equal(t, tc.want, fixed.Text)
			again, err := tooling.Lint(path, fixed.Text, true)
			require.NoError(t, err)
			require.Equal(t, fixed, again)
			if tc.name != "quoted.yaml" {
				require.Empty(t, fixed.Diagnostics)
			}
		})
	}
	advisory := "let unused = 1\nroot = {foo: this.foo}\n"
	result, err := tooling.Lint(filepath.Join(dir, "advisory.blobl"), advisory, true)
	require.NoError(t, err)
	require.Equal(t, advisory, result.Text)
	require.NotEmpty(t, result.Diagnostics)
}

func TestToolingDoesNotReadSamplesOrExecuteConstructors(t *testing.T) {
	tooling, dir := testTooling(t)
	constructorCalls := 0
	executions := 0
	// Install a private plugin whose constructor and runtime function both have
	// observable effects. Static tooling must invoke neither, even with imports.
	env := benthosEnvironmentForToolTest(t, &constructorCalls, &executions)
	tooling.h.benv = env.Deactivated()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.blobl"), []byte("map identity { root = tooling_probe() }\n"), 0600))
	text := "#!sample_from missing.json\nimport \"shared.blobl\"\nroot = tooling_probe()\nroot.env = env(\"UNSET_TOOLING_TEST\")\n"
	for _, name := range []string{"mapping.blobl", "mapping.yaml"} {
		input := text
		if strings.HasSuffix(name, ".yaml") {
			input = "# bloblang-sample: missing.json\nmapping: |-\n  " + strings.ReplaceAll(strings.TrimSuffix(text, "\n"), "\n", "\n  ") + "\n"
		}
		result, err := tooling.Lint(filepath.Join(dir, name), input, true)
		require.NoError(t, err)
		require.Len(t, result.Diagnostics, 1)
		require.Equal(t, "correctness/environment/require-fallback", result.Diagnostics[0].Code)
		_, err = tooling.Format(filepath.Join(dir, name), input, 2)
		require.NoError(t, err)
	}
	require.Zero(t, constructorCalls)
	require.Zero(t, executions)
}

func benthosEnvironmentForToolTest(t *testing.T, constructors, executions *int) *bloblang.Environment {
	t.Helper()
	env := bloblang.GlobalEnvironment().Clone()
	err := env.RegisterFunctionV2("tooling_probe", bloblang.NewPluginSpec().Impure(), func(*bloblang.ParsedParams) (bloblang.Function, error) {
		*constructors++
		return func() (any, error) { *executions++; return "probe", nil }, nil
	})
	require.NoError(t, err)
	return env
}

func TestToolingUnicodeDiagnosticColumnsAndClose(t *testing.T) {
	tooling, dir := testTooling(t)
	text := fmt.Sprintf("root = {label: %q, value: env(\"THING\")}\n", "😀")
	result, err := tooling.Lint(filepath.Join(dir, "unicode.blobl"), text, false)
	require.NoError(t, err)
	require.Len(t, result.Diagnostics, 1)
	require.Equal(t, 1, result.Diagnostics[0].Line)
	require.Equal(t, 28, result.Diagnostics[0].Column)
	tooling.Close()
	tooling.Close()
	_, err = tooling.Lint(filepath.Join(dir, "closed.blobl"), "root = this", false)
	require.ErrorContains(t, err, "closed")
	_, err = tooling.Format(filepath.Join(dir, "closed.blobl"), "root = this", 2)
	require.ErrorContains(t, err, "closed")
}

func TestToolingAndEditorNestedSettingsParity(t *testing.T) {
	tooling, root := testTooling(t)
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0700))
	child := filepath.Join(root, "nested")
	require.NoError(t, os.Mkdir(child, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".bloblangrc.json"), []byte(`{"formatter":{"printWidth":100},"lint":{"rules":{"correctness/variables/no-unused-let":"off"}}}`), 0600))
	configPath := filepath.Join(child, ".bloblangrc.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"formatter":{"printWidth":20},"lint":{"rules":{"correctness/variables/no-unused-let":"error"}}}`), 0600))
	editor, _ := featureHandler(t)
	editor.workspaceRoot = root
	t.Cleanup(func() { editor.parser.Close("") })
	path := filepath.Join(child, "mapping.blobl")
	uri := fileURI(path)
	text := "let unused = 1\nroot = {foo: this.foo, bar: this.bar, baz: this.baz}\n"
	cliFormatted, err := tooling.Format(path, text, 2)
	require.NoError(t, err)
	editorFormatted, ok := editor.formatText(uri, text, 2)
	require.True(t, ok)
	require.Equal(t, editorFormatted, cliFormatted)
	cliLint, err := tooling.Lint(path, text, false)
	require.NoError(t, err)
	editorLint := editor.lintFindings(uri, text)
	for _, diagnostic := range cliLint.Diagnostics {
		if diagnostic.Code == "correctness/variables/no-unused-let" {
			require.Equal(t, "error", diagnostic.Severity)
		}
	}
	require.Len(t, editorLint, len(cliLint.Diagnostics))
	require.NoError(t, os.WriteFile(configPath, []byte(`{"unknown":true}`), 0600))
	_, err = tooling.Lint(path, text, false)
	require.ErrorContains(t, err, configPath)
	_, err = tooling.Format(path, text, 2)
	require.ErrorContains(t, err, configPath)
	require.Equal(t, editorconfig.Default(), editor.workspaceConfig(uri))
	require.NotEmpty(t, editor.lintFindings(uri, text), "static editor features survive invalid configuration")
	_, ok = editor.formatText(uri, text, 2)
	require.True(t, ok)
}

func TestToolingAndEditorMappingRootParityAndNoStaleContext(t *testing.T) {
	tooling, root := testTooling(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: somewhere\n"), 0600))
	child := filepath.Join(root, "nested")
	require.NoError(t, os.Mkdir(child, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "shared.blobl"), []byte("root = this\n"), 0600))
	editor, _ := featureHandler(t)
	editor.workspaceRoot = root
	t.Cleanup(func() { editor.parser.Close("") })
	text := "mapping: from \"shared.blobl\"\n"
	path := filepath.Join(child, "pipeline.yaml")
	result, err := tooling.Lint(path, text, false)
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)
	require.Empty(t, editor.yamlPathDiagnostics(fileURI(path), text))
	formatted, err := tooling.Format(path, text, 2)
	require.NoError(t, err)
	require.Equal(t, text, formatted)
	loosePath := filepath.Join(t.TempDir(), "pipeline.yaml")
	result, err = tooling.Lint(loosePath, text, false)
	require.NoError(t, err)
	require.Len(t, result.Diagnostics, 1, "previous project's fallback must not leak")
	require.Contains(t, result.Diagnostics[0].Message, filepath.Dir(loosePath))
	require.Empty(t, tooling.h.workspaceRoot)
}

func TestEditorMappingUsesContainingWorkspaceRoot(t *testing.T) {
	h, _ := featureHandler(t)
	t.Cleanup(func() { h.parser.Close("") })
	primary := h.workspaceRoot
	second := t.TempDir()
	nested := filepath.Join(second, "nested")
	require.NoError(t, os.Mkdir(nested, 0700))
	h.workspaceRoots = []string{primary, second, nested}
	for _, root := range []string{primary, second, nested} {
		require.NoError(t, os.WriteFile(filepath.Join(root, "shared.blobl"), []byte("root = this\n"), 0600))
	}
	uri := fileURI(filepath.Join(nested, "sub", "pipeline.yaml"))
	require.Equal(t, filepath.Join(nested, "shared.blobl"), h.resolveMappingPath(uri, "shared.blobl"))
	require.NoError(t, os.Remove(filepath.Join(nested, "shared.blobl")))
	require.Equal(t, filepath.Join(nested, "sub", "shared.blobl"), h.resolveMappingPath(uri, "shared.blobl"), "must not fall back to another workspace")
}

func TestToolingClearsParserCachesBetweenFiles(t *testing.T) {
	tooling, dir := testTooling(t)
	for i := 0; i < 20; i++ {
		path := filepath.Join(dir, fmt.Sprintf("mapping-%d.yaml", i))
		text := "mapping: |-\n  root={a:1}\n"
		_, err := tooling.Format(path, text, 2)
		require.NoError(t, err)
		require.Zero(t, reflect.ValueOf(tooling.h.parser).Elem().FieldByName("trees").Len())
		_, err = tooling.Lint(path, text, true)
		require.NoError(t, err)
		require.Zero(t, reflect.ValueOf(tooling.h.parser).Elem().FieldByName("trees").Len())
	}
}

func TestEditorAbsoluteImportParity(t *testing.T) {
	h, uri := featureHandler(t)
	t.Cleanup(func() { h.parser.Close("") })
	importPath := filepath.Join(t.TempDir(), "shared.blobl")
	require.NoError(t, os.WriteFile(importPath, []byte("map identity { root = this }\n"), 0600))
	text := fmt.Sprintf("import %q\nroot = this.apply(\"identity\")\n", importPath)
	require.Empty(t, h.diagnosticsText(uri, text))
	_, ok := h.formatText(uri, text, 2)
	require.True(t, ok)
	locations, err := h.definitionForText(uri, text, protocol.Position{Line: 0, Character: 10})
	require.NoError(t, err)
	require.Len(t, locations, 1)
	require.Equal(t, fileURI(importPath), locations[0].URI)
}

func TestEditorEscapedImportPaths(t *testing.T) {
	h, uri := featureHandler(t)
	t.Cleanup(func() { h.parser.Close("") })
	for _, tc := range []struct{ literal, path string }{
		{`"C:\\Users\\developer\\shared.blobl"`, `C:\Users\developer\shared.blobl`},
		{`"\\\\server\\share\\shared.blobl"`, `\\server\share\shared.blobl`},
		{`"shared\u0020mapping.blobl"`, "shared mapping.blobl"},
		{`"shared\\mapping.blobl"`, `shared\mapping.blobl`},
	} {
		t.Run(tc.literal, func(t *testing.T) {
			require.Equal(t, tc.path, decodeImportPath(tc.literal))
			text := "import " + tc.literal + "\nroot = this\n"
			locations, err := h.definitionForText(uri, text, protocol.Position{Line: 0, Character: 10})
			require.NoError(t, err)
			require.Len(t, locations, 1)
			require.Equal(t, fileURI(h.resolveImportPath(uri, tc.path)), locations[0].URI)
		})
	}
}
