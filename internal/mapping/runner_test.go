package mapping

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunMessageSemantics(t *testing.T) {
	for _, tt := range []struct {
		name, source, input, root, output string
		metadata                          map[string]any
		deleted                           bool
	}{
		{name: "object", source: `root = {"value": this.value.uppercase()}`, input: `{"value":"hello"}`, output: `{"value":"HELLO"}`},
		{name: "null input", source: `root = this`, input: `null`, output: `null`},
		{name: "null output", source: `root = null`, input: `{}`, output: `null`},
		{name: "deleted", source: `root = deleted()`, input: `{}`, output: `null`, deleted: true},
		{name: "untouched", source: `let name = "x"`, input: `{"original":true}`, output: `{"original":true}`},
		{name: "string", source: `root = "hello"`, input: `{}`, output: `"hello"`},
		{name: "bytes", source: `root = "hello".bytes()`, input: `{}`, output: `"hello"`},
		{name: "payload JSON", source: `root = "{\"value\":42}"`, input: `{}`, output: `{"value":42}`},
		{name: "large integer", source: `root = this`, input: `9223372036854775807`, output: `9223372036854775807`},
		{name: "numeric operation", source: `root = this.value + 2`, input: `{"value":40}`, output: `42`},
		{name: "root mutation", source: "root.seen = this.value\nroot.original = root.keep", input: `{"value":42}`, root: `{"keep":"yes"}`, output: `{"keep":"yes","seen":42,"original":"yes"}`},
		{name: "explicit null root", source: `root = root`, input: `{"value":42}`, root: `null`, output: `null`},
		{name: "metadata", source: "meta copied = @original\nmeta original = deleted()\nroot = this", input: `{}`, output: `{}`, metadata: map[string]any{"original": "x"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := Run(Request{Source: tt.source, Input: json.RawMessage(tt.input), Root: json.RawMessage(tt.root), Metadata: tt.metadata})
			require.Nil(t, result.Error, "%+v", result.Error)
			encoded, err := json.Marshal(result.Output)
			require.NoError(t, err)
			assert.JSONEq(t, tt.output, string(encoded))
			assert.Equal(t, tt.deleted, result.Deleted)
			if tt.name == "payload JSON" {
				assert.Equal(t, &RawOutput{Encoding: "utf8", Data: `{"value":42}`}, result.RawOutput)
			}
			if tt.name == "metadata" {
				assert.Equal(t, map[string]any{"copied": "x"}, result.Metadata)
			}
		})
	}
}

func TestRunBinaryOutput(t *testing.T) {
	result := Run(Request{Source: `root = "/w==".decode("base64")`, Input: json.RawMessage(`null`)})
	require.Nil(t, result.Error, "%+v", result.Error)
	assert.Equal(t, &RawOutput{Encoding: "base64", Data: "/w=="}, result.RawOutput)
}

func TestRunBoundsMapRecursion(t *testing.T) {
	result := Run(Request{Source: "map recurse { root = this.apply(\"recurse\") }\nroot = this.apply(\"recurse\")", Input: json.RawMessage(`null`)})
	require.NotNil(t, result.Error)
	assert.Equal(t, "execute", result.Error.Kind)
	assert.Contains(t, result.Error.Message, "recursion")
}

func TestRunEnvironmentAndImports(t *testing.T) {
	t.Setenv("BLOBLANG_RUNNER_HOST_SECRET", "host-value")
	result := Run(Request{
		Source:  "import \"helpers.blobl\"\nroot = this.apply(\"annotate\")",
		Input:   json.RawMessage(`{}`),
		Env:     map[string]string{"EMPTY": "", "PRESENT": "fixture"},
		Imports: map[string]string{"helpers.blobl": `map annotate { root = {"missing": env("BLOBLANG_RUNNER_HOST_SECRET"), "empty": env("EMPTY"), "present": env(name: "PRESENT", no_cache: true)} }`},
	})
	require.Nil(t, result.Error, "%+v", result.Error)
	encoded, err := json.Marshal(result.Output)
	require.NoError(t, err)
	assert.JSONEq(t, `{"missing":null,"empty":"","present":"fixture"}`, string(encoded))
	missing := Run(Request{Source: "import \"/etc/passwd\"\nroot = this", Input: json.RawMessage(`null`)})
	require.NotNil(t, missing.Error)
	assert.Equal(t, "parse", missing.Error.Kind)
	assert.Contains(t, missing.Error.Message, "was not supplied")
	for _, source := range []string{`root = file("/etc/passwd")`, `root = range(0, 1).map_each(x -> file("/etc/passwd"))`} {
		result := Run(Request{Source: source, Input: json.RawMessage(`null`)})
		require.NotNil(t, result.Error)
		assert.Equal(t, "parse", result.Error.Kind)
	}
}

func TestRunErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		req  Request
		kind string
	}{
		{"missing input", Request{Source: `root = 1`}, "input"},
		{"invalid input", Request{Source: `root = this`, Input: json.RawMessage(`no`)}, "input"},
		{"extra input", Request{Source: `root = this`, Input: json.RawMessage(`{} {}`)}, "input"},
		{"invalid root", Request{Source: `root = this`, Input: json.RawMessage(`{}`), Root: json.RawMessage(`no`)}, "input"},
		{"invalid metadata", Request{Source: `root = this`, Input: json.RawMessage(`{}`), Metadata: map[string]any{"bad": func() {}}}, "input"},
		{"parse", Request{Source: "root = (", Input: json.RawMessage(`{}`)}, "parse"},
		{"execute", Request{Source: `root = throw("deliberate")`, Input: json.RawMessage(`{}`)}, "execute"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := Run(tt.req)
			require.NotNil(t, result.Error)
			assert.Equal(t, tt.kind, result.Error.Kind)
			assert.NotEmpty(t, result.Error.Message)
			assert.Nil(t, result.Output)
			if tt.kind == "parse" {
				assert.Equal(t, 1, result.Error.Line)
				assert.Positive(t, result.Error.Column)
			}
		})
	}
}

func TestRunDoesNotMutateFixtures(t *testing.T) {
	meta := map[string]any{"nested": map[string]any{"before": "yes"}}
	input := json.RawMessage(`{"list":[1,2]}`)
	root := json.RawMessage(`{"keep":true}`)
	result := Run(Request{Source: "meta nested = @nested.assign({\"after\": true})\nroot.copied = this.list", Input: input, Root: root, Metadata: meta})
	require.Nil(t, result.Error, "%+v", result.Error)
	assert.Equal(t, map[string]any{"nested": map[string]any{"before": "yes"}}, meta)
	assert.Equal(t, `{"list":[1,2]}`, string(input))
	assert.Equal(t, `{"keep":true}`, string(root))
}
