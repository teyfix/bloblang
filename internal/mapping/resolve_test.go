package mapping

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redpanda-data/benthos/v4/public/bloblang"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeResolveFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestResolveMappingAndLargeInputFiles(t *testing.T) {
	dir := t.TempDir()
	mapping := writeResolveFile(t, dir, "mapping.blobl", "root = this\n")
	input := `{"payload":"` + strings.Repeat("a", 120_000) + `","integer":9223372036854775807}`
	inputPath := writeResolveFile(t, dir, "input.json", input)
	t.Chdir(dir)
	req, err := Resolve(Request{MappingPath: filepath.Base(mapping), InputPath: filepath.Base(inputPath)})
	require.NoError(t, err)
	assert.Equal(t, "root = this\n", req.Source)
	assert.Equal(t, input, string(req.Input))
	assert.Empty(t, req.MappingPath)
	assert.Empty(t, req.InputPath)
	assert.Empty(t, req.SamplePath)
	assert.Equal(t, mapping, req.Origin)
	serialized, err := json.Marshal(req)
	require.NoError(t, err)
	assert.NotContains(t, string(serialized), mapping)
	result := Run(req)
	require.Nil(t, result.Error)
	encoded, err := json.Marshal(result.Output)
	require.NoError(t, err)
	assert.JSONEq(t, input, string(encoded))
}

func TestResolveSnapshotsTransitiveImports(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o700))
	main := writeResolveFile(t, dir, "main.blobl", "import \"sub/a.blobl\"\nroot = this.apply(\"a\")")
	a := "import \"../b.blobl\"\nmap a { root = this.apply(\"b\") }"
	b := `map b { root = this.uppercase() }`
	writeResolveFile(t, dir, "sub/a.blobl", a)
	writeResolveFile(t, dir, "b.blobl", b)
	req, err := Resolve(Request{MappingPath: main, Input: json.RawMessage(`"hello"`)})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"sub/a.blobl": a, "b.blobl": b}, req.Imports)
	// Execution must use the snapshot after all source files disappear and cwd
	// changes, since the isolated worker has no filesystem importer.
	require.NoError(t, os.Remove(filepath.Join(dir, "sub/a.blobl")))
	require.NoError(t, os.Remove(filepath.Join(dir, "b.blobl")))
	t.Chdir(t.TempDir())
	result := Run(req)
	require.Nil(t, result.Error, "%+v", result.Error)
	assert.Equal(t, "HELLO", result.Output)
}

func TestSnapshotImportsForStaticTools(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "virtual.blobl")
	source := "import \"helper.blobl\"\nroot = this.apply(\"helper\")"
	helper := `map helper { root = this.uppercase() }`
	writeResolveFile(t, dir, "helper.blobl", helper)
	imports, err := SnapshotImports(source, path, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"helper.blobl": helper}, imports)
	imports, err = SnapshotImports(source, "", nil)
	require.NoError(t, err)
	assert.Empty(t, imports)
	supplied := map[string]string{"helper.blobl": "fixture"}
	imports, err = SnapshotImports(source, "", supplied)
	require.NoError(t, err)
	imports["helper.blobl"] = "mutated"
	assert.Equal(t, "fixture", supplied["helper.blobl"])
	_, err = SnapshotImports(strings.Repeat("x", MaxRequestBytes), "", nil)
	require.ErrorContains(t, err, "exceeds")
}

func TestResolveRejectsNonregularFiles(t *testing.T) {
	dir := t.TempDir()
	_, err := Resolve(Request{MappingPath: dir, Input: json.RawMessage(`null`)})
	require.ErrorContains(t, err, "not a regular file")
	_, err = Resolve(Request{Source: "root = this", InputPath: dir})
	require.ErrorContains(t, err, "not a regular file")
	_, err = SnapshotImports(`import "."`, filepath.Join(dir, "main.blobl"), nil)
	require.ErrorContains(t, err, "not a regular file")
}

func TestResolveExplicitImportsOverrideFiles(t *testing.T) {
	dir := t.TempDir()
	main := writeResolveFile(t, dir, "main.blobl", "import \"helper.blobl\"\nroot = this.apply(\"helper\")")
	writeResolveFile(t, dir, "helper.blobl", `map helper { root = "filesystem" }`)
	imports := map[string]string{"helper.blobl": `map helper { root = "fixture" }`}
	req, err := Resolve(Request{MappingPath: main, Input: json.RawMessage(`null`), Imports: imports})
	require.NoError(t, err)
	result := Run(req)
	require.Nil(t, result.Error)
	assert.Equal(t, "fixture", result.Output)
	req.Imports["helper.blobl"] = "modified"
	assert.Equal(t, `map helper { root = "fixture" }`, imports["helper.blobl"])
}

func TestResolveImportsErrorsAndLimits(t *testing.T) {
	dir := t.TempDir()
	main := writeResolveFile(t, dir, "main.blobl", "import \"missing.blobl\"\nroot = this")
	_, err := Resolve(Request{MappingPath: main, Input: json.RawMessage(`null`)})
	require.ErrorContains(t, err, `import "missing.blobl"`)

	main = writeResolveFile(t, dir, "main.blobl", "import \"a.blobl\"\nroot = this")
	writeResolveFile(t, dir, "a.blobl", "import \"b.blobl\"\nmap a { root = this }")
	writeResolveFile(t, dir, "b.blobl", "import \"a.blobl\"\nmap b { root = this }")
	_, err = Resolve(Request{MappingPath: main, Input: json.RawMessage(`null`)})
	require.ErrorContains(t, err, "possible import cycle")

	large := "#" + strings.Repeat("x", MaxRequestBytes/2) + "\n"
	main = writeResolveFile(t, dir, "main.blobl", "import \"a.blobl\"\nimport \"b.blobl\"\nroot = this")
	writeResolveFile(t, dir, "a.blobl", large+"map a { root = this }")
	writeResolveFile(t, dir, "b.blobl", large+"map b { root = this }")
	_, err = Resolve(Request{MappingPath: main, Input: json.RawMessage(`null`)})
	require.ErrorContains(t, err, "exceeds")
}

func TestResolveDefersParseErrorsAndDisablesConstructors(t *testing.T) {
	dir := t.TempDir()
	for _, source := range []string{
		"root = (",
		`root = file("/this-file-must-not-be-read-by-the-resolver")`,
	} {
		main := writeResolveFile(t, dir, "main.blobl", source)
		req, err := Resolve(Request{MappingPath: main, Input: json.RawMessage(`null`)})
		require.NoError(t, err)
		result := Run(req)
		require.NotNil(t, result.Error)
		assert.Equal(t, "parse", result.Error.Kind)
	}
}

func TestResolveImportSnapshotDoesNotInvokeConstructors(t *testing.T) {
	constructors := 0
	err := bloblang.RegisterFunctionV2("mapping_runner_import_probe", bloblang.NewPluginSpec().Impure(), func(*bloblang.ParsedParams) (bloblang.Function, error) {
		constructors++
		return func() (any, error) { return "unexpected", nil }, nil
	})
	require.NoError(t, err)
	dir := t.TempDir()
	main := writeResolveFile(t, dir, "main.blobl", "import \"helper.blobl\"\nroot = this.apply(\"helper\")")
	writeResolveFile(t, dir, "helper.blobl", "root = mapping_runner_import_probe()\nmap helper { root = mapping_runner_import_probe() }")
	req, err := Resolve(Request{MappingPath: main, Input: json.RawMessage(`null`)})
	require.NoError(t, err)
	assert.Zero(t, constructors)
	result := Run(req)
	require.NotNil(t, result.Error)
	assert.Equal(t, "parse", result.Error.Kind)
	assert.Zero(t, constructors)
}

func TestResolveInlineSourceDoesNotReadImports(t *testing.T) {
	path := writeResolveFile(t, t.TempDir(), "helper.blobl", `map helper { root = "filesystem" }`)
	name, err := json.Marshal(path)
	require.NoError(t, err)
	req, err := Resolve(Request{Source: "import " + string(name) + "\nroot = this.apply(\"helper\")", Input: json.RawMessage(`null`)})
	require.NoError(t, err)
	assert.Empty(t, req.Imports)
	result := Run(req)
	require.NotNil(t, result.Error)
	assert.Contains(t, result.Error.Message, "was not supplied")
}

func TestResolveDirectMappingImport(t *testing.T) {
	dir := t.TempDir()
	main := writeResolveFile(t, dir, "main.blobl", `from "target.blobl"`)
	writeResolveFile(t, dir, "target.blobl", `root = this.uppercase()`)
	req, err := Resolve(Request{MappingPath: main, Input: json.RawMessage(`"hello"`)})
	require.NoError(t, err)
	result := Run(req)
	require.Nil(t, result.Error, "%+v", result.Error)
	assert.Equal(t, "HELLO", result.Output)
}

func TestResolveInputYAML(t *testing.T) {
	path := writeResolveFile(t, t.TempDir(), "input.yaml", "name: Bob\nitems: [1, false, null]\nlarge: 9223372036854775807\n")
	req, err := Resolve(Request{Source: "root = this", InputPath: path})
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"Bob","items":[1,false,null],"large":9223372036854775807}`, string(req.Input))
}

func TestResolveRejectsOversizedFiles(t *testing.T) {
	path := writeResolveFile(t, t.TempDir(), "oversized", "")
	require.NoError(t, os.Truncate(path, MaxRequestBytes+1))
	_, err := Resolve(Request{MappingPath: path, Input: json.RawMessage("null")})
	require.ErrorContains(t, err, "exceeds maximum request size")
	_, err = Resolve(Request{Source: "root = this", InputPath: path})
	require.ErrorContains(t, err, "exceeds maximum request size")
}

func TestResolveRejectsInvalidInputFiles(t *testing.T) {
	for _, test := range []struct{ name, file, content, message string }{
		{"invalid YAML", "input.yaml", "value: [", "decode YAML"},
		{"extra YAML document", "input.yaml", "value: 1\n---\nvalue: 2\n", "exactly one YAML document"},
		{"empty YAML", "input.yaml", "", "decode YAML"},
		{"nonstring keys", "input.yaml", "nested:\n  1: value\n", "JSON-compatible"},
		{"duplicate keys", "input.yaml", "key: 1\nkey: 2\n", "already defined"},
		{"infinity", "input.yaml", "value: .inf\n", "JSON-compatible"},
		{"extra JSON", "input.json", "{} {}", "exactly one JSON value"},
		{"invalid JSON", "input.json", "value: 1", "invalid JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeResolveFile(t, t.TempDir(), test.file, test.content)
			_, err := Resolve(Request{Source: "root = this", InputPath: path})
			require.ErrorContains(t, err, test.message)
		})
	}
}

func TestResolveSiblingSamples(t *testing.T) {
	for _, ext := range []string{"json", "yaml", "yml"} {
		t.Run(ext, func(t *testing.T) {
			dir := t.TempDir()
			mapping := writeResolveFile(t, dir, "my mapping.blobl", "root = this\n")
			writeResolveFile(t, dir, "my mapping.sample."+ext, `{"$bloblang":{"input":{"value":42},"meta":{"origin":"sample"},"env":{"TOKEN":"fixture"},"root":null}}`)
			req, err := Resolve(Request{MappingPath: mapping})
			require.NoError(t, err)
			assert.JSONEq(t, `{"value":42}`, string(req.Input))
			assert.Equal(t, map[string]any{"origin": "sample"}, req.Metadata)
			assert.Equal(t, map[string]string{"TOKEN": "fixture"}, req.Env)
			assert.Equal(t, "null", string(req.Root))
			assert.Empty(t, req.MappingPath)
		})
	}
}

func TestResolveSampleOverrides(t *testing.T) {
	dir := t.TempDir()
	mapping := writeResolveFile(t, dir, "mapping.blobl", "#!input {\"inline\":true}\nroot = this\n")
	writeResolveFile(t, dir, "selected.yaml", "$bloblang:\n  input: {file: true}\n  root: {keep: true}\n  meta: {origin: sample}\n  env: {TOKEN: fixture}\n")
	req, err := Resolve(Request{
		MappingPath: mapping, SamplePath: "selected.yaml",
		Metadata: map[string]any{}, Env: map[string]string{}, Root: json.RawMessage("null"),
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"file":true}`, string(req.Input))
	assert.Empty(t, req.Metadata)
	assert.NotNil(t, req.Metadata)
	assert.Empty(t, req.Env)
	assert.NotNil(t, req.Env)
	assert.Equal(t, "null", string(req.Root))
	assert.Empty(t, req.SamplePath)
}

func TestResolveMappingRelativeDirectives(t *testing.T) {
	dir := t.TempDir()
	mapping := writeResolveFile(t, dir, "mapping.blobl", "#!sample_from fixture.yaml\nroot = this\n")
	writeResolveFile(t, dir, "fixture.yaml", "$bloblang:\n  input: null\n  meta: {origin: directive}\n  env: {TOKEN: fixture}\n")
	req, err := Resolve(Request{MappingPath: mapping})
	require.NoError(t, err)
	assert.Equal(t, "null", string(req.Input))
	assert.Equal(t, map[string]any{"origin": "directive"}, req.Metadata)
	assert.Equal(t, map[string]string{"TOKEN": "fixture"}, req.Env)
}

func TestResolveInlineSample(t *testing.T) {
	req, err := Resolve(Request{Source: "#!sample {\"input\": null, \"root\": null, \"meta\": {\"origin\": \"inline\"}, \"env\": {\"TOKEN\": \"fixture\"}}\nroot = this\n"})
	require.NoError(t, err)
	assert.Equal(t, "null", string(req.Input))
	assert.Equal(t, "null", string(req.Root))
	assert.Equal(t, map[string]any{"origin": "inline"}, req.Metadata)
	assert.Equal(t, map[string]string{"TOKEN": "fixture"}, req.Env)
}

func TestResolveInlineSourceExplicitSample(t *testing.T) {
	dir := t.TempDir()
	path := writeResolveFile(t, dir, "selected.yaml", "$bloblang:\n  input: {selected: true}\n")
	t.Chdir(dir)
	req, err := Resolve(Request{Source: "root = this", SamplePath: filepath.Base(path)})
	require.NoError(t, err)
	assert.JSONEq(t, `{"selected":true}`, string(req.Input))
	assert.Empty(t, req.SamplePath)
}

func TestResolveInlineSourceHasNoImplicitWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".sample.json", "bloblang-run.sample.json", "mapping.sample.json"} {
		writeResolveFile(t, dir, name, `{"$bloblang":{"input":"incidental"}}`)
	}
	path := writeResolveFile(t, dir, "explicit.json", `{"$bloblang":{"input":"explicit"}}`)
	t.Chdir(dir)
	_, err := Resolve(Request{Source: "root = this"})
	require.ErrorContains(t, err, "no input supplied or sample found")
	_, err = Resolve(Request{Source: "#!sample_from explicit.json\nroot = this"})
	require.ErrorContains(t, err, "requires mappingPath or an explicit samplePath")
	req, err := Resolve(Request{Source: "#!sample_from " + path + "\nroot = this"})
	require.NoError(t, err)
	assert.Equal(t, `"explicit"`, string(req.Input))
}

func TestResolveExplicitInputSkipsSampleDiscovery(t *testing.T) {
	dir := t.TempDir()
	mapping := writeResolveFile(t, dir, "mapping.blobl", "#!sample_from missing.yaml\nroot = this")
	writeResolveFile(t, dir, "mapping.sample.json", `{"$bloblang":{"input":"sample"}}`)
	writeResolveFile(t, dir, "mapping.sample.yaml", "$bloblang:\n  input: ambiguous\n")
	req, err := Resolve(Request{MappingPath: mapping, Input: json.RawMessage("null")})
	require.NoError(t, err)
	assert.Equal(t, "null", string(req.Input))
}

func TestResolveErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		req  Request
		want string
	}{
		{"missing source", Request{}, "source or mappingPath is required"},
		{"source conflict", Request{Source: "root = this", MappingPath: "unused.blobl"}, "source and mappingPath are mutually exclusive"},
		{"input conflict", Request{Source: "root = this", Input: json.RawMessage("null"), InputPath: "unused.json"}, "input, inputPath and samplePath are mutually exclusive"},
		{"sample conflict", Request{Source: "root = this", InputPath: "unused.json", SamplePath: "unused.yaml"}, "input, inputPath and samplePath are mutually exclusive"},
		{"inline sample conflict", Request{Source: "root = this", Input: json.RawMessage("null"), SamplePath: "unused.yaml"}, "input, inputPath and samplePath are mutually exclusive"},
		{"missing input", Request{Source: "root = this"}, "no input supplied or sample found"},
		{"missing mapping file", Request{MappingPath: filepath.Join(t.TempDir(), "missing.blobl")}, "mappingPath"},
		{"missing input file", Request{Source: "root = this", InputPath: filepath.Join(t.TempDir(), "missing.json")}, "inputPath"},
		{"missing sample file", Request{Source: "root = this", SamplePath: filepath.Join(t.TempDir(), "missing.yaml")}, "sample"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Resolve(test.req)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestResolveSampleRequiresEnvelope(t *testing.T) {
	path := writeResolveFile(t, t.TempDir(), "sample.yaml", "input: true\n")
	_, err := Resolve(Request{Source: "root = this", SamplePath: path})
	require.ErrorContains(t, err, "missing mandatory $bloblang envelope")
}
