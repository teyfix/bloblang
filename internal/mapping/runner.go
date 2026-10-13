// Package mapping executes standalone Bloblang mappings against explicit fixtures.
package mapping

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/redpanda-data/benthos/v4/public/bloblang"
	"github.com/redpanda-data/benthos/v4/public/service"
	"github.com/teyfix/bloblang-lsp/internal/benthos"
)

// Request contains everything a mapping can read. Input is required: JSON null
// is a valid input and differs from an omitted input. Root optionally supplies a
// mutable output document, while this continues to refer to Input.
// Imports maps exact import names to source text; no filesystem is consulted.
type Request struct {
	Source      string            `json:"source"`
	Input       json.RawMessage   `json:"input"`
	Metadata    map[string]any    `json:"metadata,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Imports     map[string]string `json:"imports,omitempty"`
	Root        json.RawMessage   `json:"root,omitempty"`
	MappingPath string            `json:"mappingPath,omitempty"`
	InputPath   string            `json:"inputPath,omitempty"`
	SamplePath  string            `json:"samplePath,omitempty"`
}

// Error identifies the failing stage. Parser positions, when present, are
// one-based. Runtime errors retain the upstream error message.
type Error struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
}

// Result follows Benthos message semantics: Output is the structured JSON
// payload when valid, otherwise the raw payload as a string. A deleted message
// has Output null and Deleted true; an ordinary JSON null has Deleted false.
// Errors never expose partial output. Metadata is empty for deleted messages.
type Result struct {
	Output   any            `json:"output"`
	Metadata map[string]any `json:"metadata"`
	Deleted  bool           `json:"deleted"`
	Error    *Error         `json:"error,omitempty"`
}

func failed(kind string, err error) Result {
	detail := &Error{Kind: kind, Message: err.Error()}
	var parseErr *bloblang.ParseError
	if errors.As(err, &parseErr) {
		detail.Line = parseErr.Line
		detail.Column = parseErr.Column
	}
	return Result{Metadata: map[string]any{}, Error: detail}
}

// Run parses and executes once using the public Benthos message API. Host I/O
// functions and methods are disabled, env reads only Request.Env (missing keys
// return null), and imports read only Request.Imports. Clock and random
// functions remain available, as in Benthos's OnlyPure environment.
//
// The upstream evaluator cannot be interrupted. Call Execute for a hard
// deadline; Run is intended for an isolated worker or trusted, bounded inputs.
func Run(req Request) Result {
	input, err := decodeValue(req.Input)
	if err != nil {
		return failed("input", fmt.Errorf("input: %w", err))
	}
	var root any
	if len(req.Root) != 0 {
		if root, err = decodeValue(req.Root); err != nil {
			return failed("input", fmt.Errorf("root: %w", err))
		}
	}
	// Round-trip metadata to accept only JSON-compatible values and isolate
	// mutable maps and arrays owned by callers from the evaluator.
	metaBytes, err := json.Marshal(req.Metadata)
	if err != nil {
		return failed("input", fmt.Errorf("metadata: %w", err))
	}
	metaValue, err := decodeValue(metaBytes)
	if err != nil {
		return failed("input", fmt.Errorf("metadata: %w", err))
	}
	meta, _ := metaValue.(map[string]any)
	env := benthos.NewEnvironment().OnlyPure().WithoutFunctions("env").WithCustomImporter(func(name string) ([]byte, error) {
		source, ok := req.Imports[name]
		if !ok {
			return nil, fmt.Errorf("import %q was not supplied in imports", name)
		}
		return []byte(source), nil
	})
	// Mirror the public env signature, including no_cache, while every access
	// remains restricted to the supplied fixture.
	err = env.RegisterFunctionV2("env", bloblang.NewPluginSpec().
		Param(bloblang.NewStringParam("name")).
		Param(bloblang.NewBoolParam("no_cache").Default(false)),
		func(params *bloblang.ParsedParams) (bloblang.Function, error) {
			name, err := params.GetString("name")
			if err != nil {
				return nil, err
			}
			return func() (any, error) {
				if value, ok := req.Env[name]; ok {
					return value, nil
				}
				return nil, nil
			}, nil
		})
	if err != nil {
		return failed("input", err)
	}
	parsed, err := env.Parse(req.Source)
	if err != nil {
		return failed("parse", err)
	}
	msg := service.NewMessage(nil)
	msg.SetStructuredMut(input)
	for key, value := range meta {
		msg.MetaSetMut(key, value)
	}
	var out *service.Message
	if len(req.Root) != 0 {
		target := service.NewMessage(nil)
		target.SetStructuredMut(root)
		// Metadata is the starting message metadata in either execution mode.
		for key, value := range meta {
			target.MetaSetMut(key, value)
		}
		out, err = target.BloblangMutateFrom(parsed, msg)
	} else {
		out, err = msg.BloblangQuery(parsed)
	}
	if err != nil {
		return failed("execute", err)
	}
	result := Result{Metadata: map[string]any{}, Deleted: out == nil}
	if out == nil {
		return result
	}
	result.Output, err = out.AsStructured()
	if err != nil {
		raw, rawErr := out.AsBytes()
		if rawErr != nil {
			return failed("output", rawErr)
		}
		result.Output = string(raw)
	}
	if err := out.MetaWalkMut(func(key string, value any) error {
		result.Metadata[key] = value
		return nil
	}); err != nil {
		return failed("output", err)
	}
	// Surface unsupported runtime values here, rather than fail a later JSON
	// response encoder after part of a response has already been written.
	if _, err := json.Marshal(result); err != nil {
		return failed("output", err)
	}
	return result
}

func decodeValue(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, errors.New("a JSON value is required (use null for null input)")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("expected exactly one JSON value")
	}
	return value, nil
}
