package benthos

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSampleValuesRequireOneCompleteDocument(t *testing.T) {
	for _, source := range []string{
		`{} {}`,
		`[] []`,
		"{}\n---\n{}",
		"{}\n---",
		"key: value\n---\nother: value",
		"{}\ntrailing garbage",
		"key: value\n---\n[unterminated",
		"",
		"# only a comment\n",
	} {
		t.Run(source, func(t *testing.T) {
			_, err := decodeSampleValue([]byte(source))
			require.Error(t, err)
		})
	}
	for _, source := range []string{
		"{\"input\":1} \n\t",
		"{\"input\":1} # YAML comment\n",
		"{\"input\":1}\n...\n# after the end marker\n",
		"input: 1\n# YAML comment\n",
	} {
		t.Run(source, func(t *testing.T) {
			value, err := decodeSampleValue([]byte(source))
			require.NoError(t, err)
			require.Contains(t, value, "input")
		})
	}
	for _, source := range []string{"null", "null # YAML comment", "~", "---"} {
		value, err := decodeSampleValue([]byte(source))
		require.NoError(t, err)
		require.Nil(t, value)
	}
	// With no format hint these are single YAML scalars, even though their
	// prefixes are valid JSON. The complete value must be preserved.
	for _, source := range []string{"123abc", "1 2", "true false"} {
		value, err := decodeSampleValue([]byte(source))
		require.NoError(t, err)
		require.Equal(t, source, value)
	}
}

func TestSampleFilesAndDirectivesRejectAdditionalDocuments(t *testing.T) {
	dir := t.TempDir()
	for _, source := range []string{
		`{"$bloblang":{"input":1}} {"$bloblang":{"input":2}}`,
		"$bloblang:\n  input: 1\n---\n$bloblang:\n  input: 2\n",
		"$bloblang:\n  input: 1\n---\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "mapping.sample.json"), []byte(source), 0600))
		for _, mapping := range []string{"root = this", "#!sample_from mapping.sample.json\nroot = this", "#!input_from mapping.sample.json\nroot = this"} {
			sample, err := ExtractSample(nil, "file:///mapping.blobl", mapping, dir)
			require.Error(t, err, source)
			require.Nil(t, sample)
		}
	}
	for _, mapping := range []string{
		"#!sample {\"input\":1} {\"input\":2}\nroot = this",
		"#!input {} {}\nroot = this",
		"#!input {}\n#!root {} {}\nroot = this",
		"#!input {}\n#!meta {} {}\nroot = this",
		"#!sample |\n#| input: 1\n#| ---\n#| input: 2\nroot = this",
		"#!input |\n#| value: 1\n#| ---\n#| value: 2\nroot = this",
	} {
		sample, err := ExtractSample(nil, "file:///inline.blobl", mapping, t.TempDir())
		require.Error(t, err, mapping)
		require.Nil(t, sample)
	}
}

func TestJSONSampleNumbersRetainPrecisionThroughPreview(t *testing.T) {
	const integer = "18446744073709551615"
	const decimal = "0.12345678901234567890123456789"
	payload := `{"id":` + integer + `,"decimal":` + decimal + `}`
	envelope := `{"input":` + payload + `,"root":` + payload + `,"meta":` + payload + `}`
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mapping.sample.json"), []byte(`{"$bloblang":`+envelope+`}`), 0600))
	for _, mapping := range []string{
		"root = this",
		"#!sample " + envelope + "\nroot = this",
		"#!input " + payload + "\n#!root " + payload + "\n#!meta " + payload + "\nroot = this",
	} {
		sample, err := ExtractSample(nil, "file:///mapping.blobl", mapping, dir)
		require.NoError(t, err)
		for _, value := range []any{sample.Value, sample.Root, sample.Meta} {
			object := value.(map[string]any)
			require.Equal(t, json.Number(integer), object["id"])
			require.Equal(t, json.Number(decimal), object["decimal"])
		}
		parser := newTestParser(t)
		executor := NewExecutor(NewEnvironment(), testExecutorConfig())
		for _, expression := range []string{"this", "root", `{"id":meta("id"),"decimal":meta("decimal")}`} {
			source := "root = " + expression
			result, err := executor.ExecuteThrough(parser, "file:///mapping.blobl", sample, source, uint(len(source)))
			require.NoError(t, err)
			require.Contains(t, result.Full, integer)
			require.Contains(t, result.Full, decimal)
		}
	}
	// json.Number fixtures remain usable by ordinary numeric Bloblang methods
	// and arithmetic; only operations, not fixture loading, choose float math.
	sample, err := ExtractSample(nil, "file:///math.blobl", "#!input {\"value\":1.25,\"count\":2}\nroot = this", t.TempDir())
	require.NoError(t, err)
	parser := newTestParser(t)
	executor := NewExecutor(NewEnvironment(), testExecutorConfig())
	source := "root = {\"sum\": this.value + 0.5, \"product\": this.value * this.count, \"kind\": this.value.type(), \"rounded\": this.value.round()}"
	result, err := executor.ExecuteThrough(parser, "file:///math.blobl", sample, source, uint(len(source)))
	require.NoError(t, err)
	require.JSONEq(t, `{"sum":1.75,"product":2.5,"kind":"number","rounded":1}`, result.Full)
}
