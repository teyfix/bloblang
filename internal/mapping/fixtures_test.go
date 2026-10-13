package mapping

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseSuiteJSONAndYAML(t *testing.T) {
	for _, data := range []string{
		`{"source":"root = this","cases":[{"name":"null","request":{"input":null},"expect":{"output":null,"metadata":{},"deleted":false}}]}`,
		"source: root = this\ncases:\n  - name: null case\n    request:\n      input: null\n    expect:\n      output: null\n      metadata: {}\n      deleted: false\n",
	} {
		suite, err := ParseSuite([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		test := suite.Cases[0]
		if string(test.Request.Input) != "null" || string(test.Expect.Output) != "null" {
			t.Fatalf("explicit null was lost: %+v", test)
		}
		if test.Expect.Metadata == nil || test.Expect.Deleted == nil || *test.Expect.Deleted {
			t.Fatalf("empty metadata or false assertion was lost: %+v", test.Expect)
		}
		results, err := RunSuite(context.Background(), suite, func(context.Context, Request) Result {
			return Result{Output: nil}
		})
		if err != nil || results.Passed != 1 || results.Failed != 0 {
			t.Fatalf("run null fixture: %+v, %v", results, err)
		}
	}
}

func TestParseSuiteRejectsInvalidFixtures(t *testing.T) {
	tests := []struct{ name, source, want string }{
		{"empty", "", "decode fixture suite"},
		{"no cases", "source: root = this\ncases: []", "at least one case"},
		{"no name", `{"source":"root = this","cases":[{"request":{"input":null},"expect":{"output":null}}]}`, "requires a name"},
		{"duplicate names", `{"source":"root = this","cases":[{"name":"a","request":{"input":null},"expect":{"output":null}},{"name":"a","request":{"input":null},"expect":{"output":null}}]}`, "duplicate fixture case name"},
		{"missing input", `{"source":"root = this","cases":[{"name":"a","request":{},"expect":{"output":null}}]}`, "explicit input"},
		{"missing source", `{"cases":[{"name":"a","request":{"input":null},"expect":{"output":null}}]}`, "requires source"},
		{"mapping path", `{"source":"root = this","cases":[{"name":"a","request":{"input":null,"mappingPath":"mapping.blobl"},"expect":{"output":null}}]}`, "must use inline source and input"},
		{"input path", `{"source":"root = this","cases":[{"name":"a","request":{"input":null,"inputPath":"input.json"},"expect":{"output":null}}]}`, "must use inline source and input"},
		{"sample path", `{"source":"root = this","cases":[{"name":"a","request":{"input":null,"samplePath":"sample.json"},"expect":{"output":null}}]}`, "must use inline source and input"},
		{"missing expectation", `{"source":"root = this","cases":[{"name":"a","request":{"input":null},"expect":{}}]}`, "at least one expectation"},
		{"empty error", `{"source":"root = this","cases":[{"name":"a","request":{"input":null},"expect":{"error":{}}}]}`, "requires kind or contains"},
		{"unknown field", `{"source":"root = this","cases":[{"name":"a","request":{"input":null},"expect":{"outputs":null}}]}`, `unknown field "outputs"`},
		{"duplicate field", "source: root = this\nsource: root = 0", `duplicate key "source"`},
		{"duplicate input key", `{"source":"root = this","cases":[{"name":"a","request":{"input":{"a":1,"a":2}},"expect":{"output":null}}]}`, `duplicate key "a"`},
		{"nonstring key", "source: root = this\ncases:\n- name: a\n  request:\n    input: {3: value}\n  expect:\n    output: null", "object keys must be strings"},
		{"multiple documents", "source: root = this\n---\nsource: root = 0", "single document"},
		{"cycle", "source: root = this\ncases: &cases [*cases]", "recursive YAML alias"},
		{"infinity", "source: root = this\ncases:\n- name: a\n  request:\n    input: .inf\n  expect:\n    output: null", "unsupported value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseSuite([]byte(test.source))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestRunSuiteDefaultsAndOverrides(t *testing.T) {
	suite, err := ParseSuite([]byte(`
source: root = this
env:
  COMMON: default
  OVERRIDE: default
imports:
  common.blobl: "map common { root = this }"
cases:
  - name: default source
    request:
      input: {count: 3}
      metadata: {label: input}
      env: {OVERRIDE: local}
      imports: {local.blobl: "map local { root = this }"}
    expect:
      output: {count: 3}
  - name: overridden source
    request:
      source: root = 42
      input: null
      root: {seed: true}
    expect:
      output: 42
`))
	if err != nil {
		t.Fatal(err)
	}
	index := 0
	results, err := RunSuite(context.Background(), suite, func(_ context.Context, request Request) Result {
		defer func() { index++ }()
		if index == 0 {
			if request.Source != suite.Source || request.Env["COMMON"] != "default" || request.Env["OVERRIDE"] != "local" || len(request.Imports) != 2 {
				t.Errorf("incorrect merged defaults: %+v", request)
			}
			if request.Metadata["label"] != "input" {
				t.Errorf("metadata lost: %+v", request.Metadata)
			}
			request.Env["COMMON"] = "mutated"
			request.Imports["common.blobl"] = "mutated"
			return Result{Output: map[string]any{"count": int64(3)}}
		}
		if request.Source != "root = 42" || request.Env["COMMON"] != "default" || len(request.Imports) != 1 || string(request.Root) != `{"seed":true}` {
			t.Errorf("incorrect second request: %+v", request)
		}
		return Result{Output: float64(42)}
	})
	if err != nil || results.Passed != 2 || results.Failed != 0 {
		t.Fatalf("run defaults: %+v, %v", results, err)
	}
	if suite.Env["COMMON"] != "default" || suite.Cases[0].Request.Env["OVERRIDE"] != "local" || suite.Imports["common.blobl"] == "mutated" {
		t.Fatal("runner mutated suite defaults")
	}
}

func TestRunSuiteExpectations(t *testing.T) {
	suite, err := ParseSuite([]byte(`
source: root = this
cases:
  - name: metadata
    request: {input: null}
    expect:
      metadata: {nested: {amount: 2}}
  - name: deletion
    request: {input: null}
    expect: {deleted: true}
  - name: expected error
    request: {input: null}
    expect:
      error: {kind: execution, contains: missing field}
  - name: mismatches
    request: {input: null}
    expect:
      output: {a: null}
      metadata: {}
      deleted: false
  - name: missing error
    request: {input: null}
    expect:
      error: {kind: parse}
`))
	if err != nil {
		t.Fatal(err)
	}
	responses := []Result{
		{Metadata: map[string]any{"nested": map[string]any{"amount": float64(2)}}},
		{Deleted: true},
		{Error: &Error{Kind: "execution", Message: "missing field: name"}},
		{Output: map[string]any{"b": nil}, Metadata: map[string]any{"extra": true}, Deleted: true, Error: &Error{Kind: "execution", Message: "oops"}},
		{Output: nil},
	}
	index := 0
	results, err := RunSuite(context.Background(), suite, func(context.Context, Request) Result {
		response := responses[index]
		index++
		return response
	})
	if err != nil || results.Passed != 3 || results.Failed != 2 || index != 5 {
		t.Fatalf("unexpected results: %+v, %v", results, err)
	}
	if len(results.Cases[3].Failures) != 4 || len(results.Cases[4].Failures) != 1 {
		t.Fatalf("missing assertion details: %+v", results.Cases)
	}
}

func TestRunSuiteCancellation(t *testing.T) {
	suite, err := ParseSuite([]byte(`{"source":"root = this","cases":[{"name":"a","request":{"input":null},"expect":{"output":null}},{"name":"b","request":{"input":null},"expect":{"output":null}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	results, err := RunSuite(ctx, suite, func(context.Context, Request) Result {
		calls++
		cancel()
		return Result{}
	})
	if !errors.Is(err, context.Canceled) || calls != 1 || len(results.Cases) != 0 {
		t.Fatalf("cancellation was not respected: %+v, %v, calls=%d", results, err, calls)
	}
	_, err = RunSuite(ctx, suite, func(context.Context, Request) Result {
		t.Fatal("called runner with canceled context")
		return Result{}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestFixtureComparisonPreservesNumbersAndObjectKeys(t *testing.T) {
	for _, pair := range [][2]json.RawMessage{
		{json.RawMessage(`{"a":[1,2e2,9007199254740993]}`), json.RawMessage(`{"a":[1.0,200.0,9007199254740993]}`)},
		{json.RawMessage(`-0.0`), json.RawMessage(`0`)},
	} {
		if failure := fixtureCompare("output", pair[0], pair[1]); failure != "" {
			t.Fatal(failure)
		}
	}
	for _, pair := range [][2]json.RawMessage{
		{json.RawMessage(`{"a":null}`), json.RawMessage(`{"b":null}`)},
		{json.RawMessage(`9007199254740993`), json.RawMessage(`9007199254740992`)},
		{json.RawMessage(`[]`), json.RawMessage(`null`)},
	} {
		if failure := fixtureCompare("output", pair[0], pair[1]); failure == "" {
			t.Fatalf("unequal values passed: %s, %s", pair[0], pair[1])
		}
	}
	if got := (Expectation{Error: &ExpectedError{Kind: "parse", Contains: "syntax"}}).check(Result{Error: &Error{Kind: "runtime", Message: "missing"}}); len(got) != 2 {
		t.Fatalf("expected both error assertions to fail: %v", got)
	}
}

func TestParseSuiteAliasesAndNumbers(t *testing.T) {
	suite, err := ParseSuite([]byte(`
source: root = this
cases:
- name: aliases
  request:
    input: &sample {number: 9007199254740993, decimal: 1.0000000000000001, date: 2026-10-13}
  expect:
    output: *sample
`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(suite.Cases[0].Request.Input, suite.Cases[0].Expect.Output) {
		t.Fatal("alias output does not match input")
	}
	if got := string(suite.Cases[0].Request.Input); got != `{"date":"2026-10-13","decimal":1.0000000000000001,"number":9007199254740993}` {
		t.Fatalf("input values were changed: %s", got)
	}
}
