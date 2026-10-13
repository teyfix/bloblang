package schema

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInferenceObservations(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{"scalar", `3`, `{"type":"float64"}`},
		{"no enums or narrow integers", `{"status":"ok","count":1}`, `{"properties":{"status":{"type":"string"},"count":{"type":"float64"}}}`},
		{"optional and nullable", `[{"id":1,"name":null},{"id":2,"name":"a","extra":true}]`, `{"elements":{"properties":{"id":{"type":"float64"},"name":{"nullable":true,"type":"string"}},"optionalProperties":{"extra":{"type":"boolean"}}}}`},
		{"empty array", `[]`, `{"elements":{}}`},
		{"empty object", `{}`, `{}`},
		{"mixed scalar array", `[1,"a",true]`, `{"elements":{}}`},
		{"mixed object array", `[{"id":1},"a"]`, `{"elements":{}}`},
		{"nullable array", `[null,"a"]`, `{"elements":{"nullable":true,"type":"string"}}`},
		{"arbitrary keys", `{"":1,"a-b":true,"a_b":"a","with space":[]}`, `{"properties":{"":{"type":"float64"},"a-b":{"type":"boolean"},"a_b":{"type":"string"},"with space":{"elements":{}}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var value any
			decoder := json.NewDecoder(strings.NewReader(tt.input))
			decoder.UseNumber()
			require.NoError(t, decoder.Decode(&value))
			result, err := Infer(value, Options{})
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(result.Schema))
			require.Equal(t, "jtd", result.Meta.Format)
			require.True(t, result.Meta.SampleBased)
			require.False(t, result.Meta.EnumInference)
			require.Equal(t, "float64", result.Meta.NumericType)
			require.NotEmpty(t, result.Meta.Warnings)
		})
	}
}

func TestMapHints(t *testing.T) {
	value := map[string]any{
		"data": map[string]any{
			"watch/providers": map[string]any{
				"US": map[string]any{"link": "a", "count": 1},
				"TR": map[string]any{"link": "b"},
			},
		},
	}
	path, err := ParseMapPath("/data/watch~1providers")
	require.NoError(t, err)
	result, err := Infer(value, Options{MapPaths: [][]string{path}})
	require.NoError(t, err)
	require.JSONEq(t, `{"properties":{"data":{"properties":{"watch/providers":{"values":{"properties":{"link":{"type":"string"}},"optionalProperties":{"count":{"type":"float64"}}}}}}}}`, string(result.Schema))
	root, err := Infer(map[string]any{"a": 1, "b": 2}, Options{MapPaths: [][]string{{}}})
	require.NoError(t, err)
	require.JSONEq(t, `{"values":{"type":"float64"}}`, string(root.Schema))
}

func TestRejectInconsistentUpstreamInference(t *testing.T) {
	// v1.0.1 forgets earlier observations for this array-first sequence.
	_, err := Infer([]any{[]any{}, 1, "x"}, Options{})
	require.ErrorContains(t, err, "inferred schema rejects input")
}

func TestInferenceLimits(t *testing.T) {
	_, err := Infer(strings.Repeat("x", MaxBytes), Options{})
	require.ErrorContains(t, err, "maximum size")
	var deep any = 1
	for range MaxDepth + 1 {
		deep = []any{deep}
	}
	_, err = Infer(deep, Options{})
	require.ErrorContains(t, err, "maximum depth")
	_, err = Infer(make([]any, MaxNulls+1), Options{})
	require.ErrorContains(t, err, "maximum null count")
	many := make([]any, MaxValues)
	for i := range many {
		many[i] = true
	}
	_, err = Infer(many, Options{})
	require.ErrorContains(t, err, "maximum value count")
	complex := make([]any, 5_000)
	for i := MaxNulls; i < len(complex); i++ {
		complex[i] = true
	}
	_, err = Infer(complex, Options{})
	require.ErrorContains(t, err, "inference complexity limit")
	_, err = Infer(func() {}, Options{})
	require.ErrorContains(t, err, "JSON-compatible")
	_, err = Infer(nil, Options{MapPaths: make([][]string, MaxMapPaths+1)})
	require.ErrorContains(t, err, "map paths")
}

func TestParseMapPath(t *testing.T) {
	for pointer, want := range map[string][]string{
		"": {}, "/": {""}, "/a~1b/~0/~01": {"a/b", "~", "~1"}, "/items/-/metadata": {"items", "-", "metadata"},
	} {
		got, err := ParseMapPath(pointer)
		require.NoError(t, err, pointer)
		require.Equal(t, want, got)
	}
	for _, pointer := range []string{"foo", "#/foo", "/~", "/~2", "/a~a"} {
		_, err := ParseMapPath(pointer)
		require.Error(t, err, pointer)
	}
}
