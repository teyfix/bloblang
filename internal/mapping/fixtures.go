package mapping

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Suite is a collection of named mapping fixtures. Source, Env and Imports are
// defaults; an individual request can override them. Environment and import
// maps are merged by key. No process environment or filesystem is consulted.
type Suite struct {
	Source  string            `json:"source,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Imports map[string]string `json:"imports,omitempty"`
	Cases   []FixtureCase     `json:"cases"`
}

type FixtureCase struct {
	Name    string      `json:"name"`
	Request Request     `json:"request"`
	Expect  Expectation `json:"expect"`
}

// Expectation checks only explicitly provided fields. Output:null asserts a
// JSON null output. Metadata checks the entire map, including an empty map.
// Errors are unexpected unless Error is provided.
type Expectation struct {
	Output   json.RawMessage `json:"output,omitempty"`
	Metadata map[string]any  `json:"metadata,omitempty"`
	Deleted  *bool           `json:"deleted,omitempty"`
	Error    *ExpectedError  `json:"error,omitempty"`
}

type ExpectedError struct {
	Kind     string `json:"kind,omitempty"`
	Contains string `json:"contains,omitempty"`
}

type SuiteResult struct {
	Passed int                 `json:"passed"`
	Failed int                 `json:"failed"`
	Cases  []FixtureCaseResult `json:"cases"`
}

type FixtureCaseResult struct {
	Name     string   `json:"name"`
	Passed   bool     `json:"passed"`
	Result   Result   `json:"result"`
	Failures []string `json:"failures,omitempty"`
}

// ParseSuite accepts one JSON or YAML document. Unknown fields, duplicate
// keys, missing inputs, duplicate case names and empty assertions are errors.
func ParseSuite(data []byte) (Suite, error) {
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return Suite{}, fmt.Errorf("decode fixture suite: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("expected a single document")
		}
		return Suite{}, fmt.Errorf("decode fixture suite: %w", err)
	}
	remaining := 100_000
	value, err := fixtureJSONValue(&document, map[*yaml.Node]bool{}, &remaining)
	if err != nil {
		return Suite{}, fmt.Errorf("decode fixture suite: %w", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return Suite{}, fmt.Errorf("encode fixture suite as JSON: %w", err)
	}
	var suite Suite
	jsonDecoder := json.NewDecoder(bytes.NewReader(encoded))
	jsonDecoder.DisallowUnknownFields()
	jsonDecoder.UseNumber()
	if err := jsonDecoder.Decode(&suite); err != nil {
		return Suite{}, fmt.Errorf("decode fixture suite: %w", err)
	}
	if err := suite.validate(); err != nil {
		return Suite{}, err
	}
	return suite, nil
}

func (s Suite) validate() error {
	if len(s.Cases) == 0 {
		return fmt.Errorf("fixture suite must contain at least one case")
	}
	names := make(map[string]bool, len(s.Cases))
	for i, test := range s.Cases {
		if strings.TrimSpace(test.Name) == "" {
			return fmt.Errorf("fixture case %d requires a name", i+1)
		}
		if names[test.Name] {
			return fmt.Errorf("duplicate fixture case name %q", test.Name)
		}
		names[test.Name] = true
		if test.Request.MappingPath != "" || test.Request.InputPath != "" || test.Request.SamplePath != "" {
			return fmt.Errorf("fixture %q must use inline source and input; mappingPath, inputPath and samplePath are not supported", test.Name)
		}
		if test.Request.Source == "" && s.Source == "" {
			return fmt.Errorf("fixture %q requires source in the suite or request", test.Name)
		}
		if len(test.Request.Input) == 0 {
			return fmt.Errorf("fixture %q requires an explicit input (use null for no input)", test.Name)
		}
		if !json.Valid(test.Request.Input) {
			return fmt.Errorf("fixture %q input must be valid JSON", test.Name)
		}
		if len(test.Request.Root) != 0 && !json.Valid(test.Request.Root) {
			return fmt.Errorf("fixture %q root must be valid JSON", test.Name)
		}
		expect := test.Expect
		if len(expect.Output) == 0 && expect.Metadata == nil && expect.Deleted == nil && expect.Error == nil {
			return fmt.Errorf("fixture %q requires at least one expectation", test.Name)
		}
		if len(expect.Output) != 0 && !json.Valid(expect.Output) {
			return fmt.Errorf("fixture %q expected output must be valid JSON", test.Name)
		}
		if expect.Error != nil && expect.Error.Kind == "" && expect.Error.Contains == "" {
			return fmt.Errorf("fixture %q expected error requires kind or contains", test.Name)
		}
	}
	return nil
}

// RunSuite executes cases in declaration order through the supplied runner.
// Assertion failures are collected, not returned as Go errors. Context
// cancellation stops the suite and returns results for completed cases.
func RunSuite(ctx context.Context, suite Suite, run func(context.Context, Request) Result) (SuiteResult, error) {
	results := SuiteResult{Cases: []FixtureCaseResult{}}
	if err := suite.validate(); err != nil {
		return results, err
	}
	if run == nil {
		return results, fmt.Errorf("fixture runner is required")
	}
	for _, test := range suite.Cases {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		request := test.Request
		if request.Source == "" {
			request.Source = suite.Source
		}
		request.Env = mergeFixtureMaps(suite.Env, request.Env)
		request.Imports = mergeFixtureMaps(suite.Imports, request.Imports)
		result := run(ctx, request)
		if err := ctx.Err(); err != nil {
			return results, err
		}
		failures := test.Expect.check(result)
		passed := len(failures) == 0
		results.Cases = append(results.Cases, FixtureCaseResult{
			Name: test.Name, Passed: passed, Result: result, Failures: failures,
		})
		if passed {
			results.Passed++
		} else {
			results.Failed++
		}
	}
	return results, nil
}

func mergeFixtureMaps(defaults, overrides map[string]string) map[string]string {
	merged := make(map[string]string, len(defaults)+len(overrides))
	for key, value := range defaults {
		merged[key] = value
	}
	for key, value := range overrides {
		merged[key] = value
	}
	return merged
}

func (e Expectation) check(result Result) []string {
	var failures []string
	if e.Error == nil && result.Error != nil {
		failures = append(failures, fmt.Sprintf("unexpected error (%s): %s", result.Error.Kind, result.Error.Message))
	} else if e.Error != nil {
		if result.Error == nil {
			failures = append(failures, "expected an error, got success")
		} else {
			if e.Error.Kind != "" && e.Error.Kind != result.Error.Kind {
				failures = append(failures, fmt.Sprintf("error kind: expected %q, got %q", e.Error.Kind, result.Error.Kind))
			}
			if e.Error.Contains != "" && !strings.Contains(result.Error.Message, e.Error.Contains) {
				failures = append(failures, fmt.Sprintf("error message: expected to contain %q, got %q", e.Error.Contains, result.Error.Message))
			}
		}
	}
	if len(e.Output) != 0 {
		if failure := fixtureCompare("output", e.Output, result.Output); failure != "" {
			failures = append(failures, failure)
		}
	}
	if e.Metadata != nil {
		actual := result.Metadata
		if actual == nil {
			actual = map[string]any{}
		}
		if failure := fixtureCompare("metadata", e.Metadata, actual); failure != "" {
			failures = append(failures, failure)
		}
	}
	if e.Deleted != nil && *e.Deleted != result.Deleted {
		failures = append(failures, fmt.Sprintf("deleted: expected %t, got %t", *e.Deleted, result.Deleted))
	}
	return failures
}

func fixtureCompare(field string, expected, actual any) string {
	wantBytes, wantErr := json.Marshal(expected)
	gotBytes, gotErr := json.Marshal(actual)
	if wantErr != nil || gotErr != nil {
		return fmt.Sprintf("%s: cannot compare JSON values (expected: %v; actual: %v)", field, wantErr, gotErr)
	}
	decode := func(data []byte) any {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		_ = decoder.Decode(&value) // Marshal above already validated the value.
		return value
	}
	if fixtureEqual(decode(wantBytes), decode(gotBytes)) {
		return ""
	}
	return fmt.Sprintf("%s: expected %s, got %s", field, wantBytes, gotBytes)
}

func fixtureEqual(expected, actual any) bool {
	switch want := expected.(type) {
	case json.Number:
		got, ok := actual.(json.Number)
		if !ok {
			return false
		}
		wantNumber, wantOK := new(big.Rat).SetString(want.String())
		gotNumber, gotOK := new(big.Rat).SetString(got.String())
		return wantOK && gotOK && wantNumber.Cmp(gotNumber) == 0
	case []any:
		got, ok := actual.([]any)
		if !ok || len(want) != len(got) {
			return false
		}
		for i := range want {
			if !fixtureEqual(want[i], got[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		got, ok := actual.(map[string]any)
		if !ok || len(want) != len(got) {
			return false
		}
		for key, value := range want {
			other, exists := got[key]
			if !exists || !fixtureEqual(value, other) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(expected, actual)
	}
}

// Decode nodes rather than map[interface{}]interface{} so non-string and
// duplicate keys cannot silently change a fixture. Alias cycles are rejected.
func fixtureJSONValue(node *yaml.Node, visiting map[*yaml.Node]bool, remaining *int) (any, error) {
	*remaining--
	if *remaining < 0 {
		return nil, fmt.Errorf("fixture suite exceeds 100000 expanded values")
	}
	if visiting[node] {
		return nil, fmt.Errorf("recursive YAML alias at line %d", node.Line)
	}
	visiting[node] = true
	defer delete(visiting, node)
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) != 1 {
			return nil, fmt.Errorf("expected one fixture document")
		}
		return fixtureJSONValue(node.Content[0], visiting, remaining)
	case yaml.AliasNode:
		return fixtureJSONValue(node.Alias, visiting, remaining)
	case yaml.MappingNode:
		result := make(map[string]any, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return nil, fmt.Errorf("object keys must be strings at line %d", key.Line)
			}
			if _, exists := result[key.Value]; exists {
				return nil, fmt.Errorf("duplicate key %q at line %d", key.Value, key.Line)
			}
			value, err := fixtureJSONValue(node.Content[i+1], visiting, remaining)
			if err != nil {
				return nil, err
			}
			result[key.Value] = value
		}
		return result, nil
	case yaml.SequenceNode:
		result := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			value, err := fixtureJSONValue(child, visiting, remaining)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		return result, nil
	case yaml.ScalarNode:
		if node.Tag == "!!timestamp" {
			return node.Value, nil // JSON has no date type; preserve the written value.
		}
		if (node.Tag == "!!int" || node.Tag == "!!float") && json.Valid([]byte(node.Value)) {
			return json.Number(node.Value), nil // Avoid float64 rounding during YAML conversion.
		}
		switch node.Tag {
		case "!!str", "!!null", "!!bool", "!!int", "!!float":
			var value any
			if err := node.Decode(&value); err != nil {
				return nil, err
			}
			return value, nil
		default:
			return nil, fmt.Errorf("unsupported YAML tag %q at line %d", node.Tag, node.Line)
		}
	default:
		return nil, fmt.Errorf("unsupported fixture value at line %d", node.Line)
	}
}
