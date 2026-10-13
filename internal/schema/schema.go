// Package schema summarizes JSON-compatible samples using the existing
// jtd-infer-go inference library. Its output describes observations, not a
// contractual schema for future data.
package schema

import (
	"encoding/json"
	"fmt"
	"strings"

	jtdinfer "github.com/bombsimon/jtd-infer-go"
	jtd "github.com/jsontypedef/json-typedef-go"
)

const (
	MaxBytes    = 4 << 20
	MaxDepth    = 128
	MaxValues   = 100_000
	MaxNulls    = 256
	MaxMapPaths = 128
	MaxWork     = 1_000_000
)

// Options contains explicit paths whose object keys represent map entries.
// Path components use the upstream library's "-" wildcard where requested.
// No map or enum hints are inferred from the sample.
type Options struct {
	MapPaths [][]string `json:"mapPaths,omitempty"`
}

type Result struct {
	Schema json.RawMessage `json:"schema"`
	Meta   Metadata        `json:"meta"`
}

type Metadata struct {
	Format        string   `json:"format"`
	SampleBased   bool     `json:"sampleBased"`
	EnumInference bool     `json:"enumInference"`
	NumericType   string   `json:"numericType"`
	Warnings      []string `json:"warnings"`
}

// Infer delegates type inference to jtd-infer-go after JSON normalization and
// bounded input validation. In particular, json.Number becomes float64, the
// representation accepted by the upstream library. Numeric values themselves
// are never included in the output. Oversized inputs fail without truncation.
func Infer(value any, opts Options) (result Result, err error) {
	// Third-party inference must not take down the CLI or language server.
	defer func() {
		if failure := recover(); failure != nil {
			result = Result{}
			err = fmt.Errorf("JTD inference failed: %v", failure)
		}
	}()
	if len(opts.MapPaths) > MaxMapPaths {
		return Result{}, fmt.Errorf("at most %d map paths are supported", MaxMapPaths)
	}
	hints := jtdinfer.WithoutHints()
	// Do not infer small integer ranges from a narrow sample.
	hints.DefaultNumType = jtdinfer.NumTypeFloat64
	for _, path := range opts.MapPaths {
		if len(path) > MaxDepth {
			return Result{}, fmt.Errorf("map path exceeds maximum depth of %d", MaxDepth)
		}
		hints.Values = hints.Values.Add(path)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return Result{}, fmt.Errorf("sample must be JSON-compatible: %w", err)
	}
	if len(encoded) > MaxBytes {
		return Result{}, fmt.Errorf("sample exceeds maximum size of %d bytes", MaxBytes)
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return Result{}, fmt.Errorf("normalize sample: %w", err)
	}
	if err := validateSize(normalized); err != nil {
		return Result{}, err
	}
	inferred := jtdinfer.NewInferrer(hints).Infer(normalized).IntoSchema()
	encoded, err = json.Marshal(inferred)
	if err != nil {
		return Result{}, fmt.Errorf("encode JTD schema: %w", err)
	}
	if len(encoded) > MaxBytes {
		return Result{}, fmt.Errorf("schema exceeds maximum size of %d bytes", MaxBytes)
	}
	// Reject unsupported upstream inference rather than returning a schema that
	// contradicts the sample. This also checks library-generated schema syntax.
	var checked jtd.Schema
	if err := json.Unmarshal(encoded, &checked); err != nil {
		return Result{}, fmt.Errorf("decode inferred JTD schema: %w", err)
	}
	if err := checked.Validate(); err != nil {
		return Result{}, fmt.Errorf("invalid inferred JTD schema: %w", err)
	}
	findings, err := jtd.Validate(checked, normalized, jtd.WithMaxErrors(1))
	if err != nil {
		return Result{}, fmt.Errorf("check inferred JTD schema: %w", err)
	}
	if len(findings) > 0 {
		return Result{}, fmt.Errorf("upstream JTD inference cannot represent this sample consistently; inferred schema rejects input at %v", findings[0].InstancePath)
	}
	return Result{
		Schema: encoded,
		Meta: Metadata{
			Format: "jtd", SampleBased: true, EnumInference: false, NumericType: "float64",
			Warnings: []string{
				"Inferred from observed samples only; required fields and types are not an authoritative contract for future data.",
				"Enums are not inferred. Numbers use float64; timestamp strings may be inferred automatically.",
				"Empty or heterogeneous values can produce an unconstrained schema ({}); mixed-type unions are not preserved.",
			},
		},
	}, nil
}

// validateSize bounds traversal only; all inference remains upstream. The null
// limit also bounds the nullable wrapper chains created by jtd-infer-go v1.0.1,
// whose repeated null observations otherwise create quadratic work.
func validateSize(value any) error {
	values, nulls := 0, 0
	var visit func(any, int) error
	visit = func(value any, depth int) error {
		if depth > MaxDepth {
			return fmt.Errorf("sample exceeds maximum depth of %d", MaxDepth)
		}
		values++
		if values > MaxValues {
			return fmt.Errorf("sample exceeds maximum value count of %d", MaxValues)
		}
		switch value := value.(type) {
		case nil:
			nulls++
			if nulls > MaxNulls {
				return fmt.Errorf("sample exceeds maximum null count of %d for bounded JTD inference", MaxNulls)
			}
		case []any:
			for _, item := range value {
				if err := visit(item, depth+1); err != nil {
					return err
				}
			}
		case map[string]any:
			for _, item := range value {
				if err := visit(item, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(value, 0); err != nil {
		return err
	}
	if values*(nulls+1) > MaxWork {
		return fmt.Errorf("sample exceeds inference complexity limit of %d (values multiplied by null count plus one)", MaxWork)
	}
	return nil
}

// ParseMapPath parses an RFC 6901 JSON Pointer. The empty pointer selects the
// root; "~1" escapes "/" and "~0" escapes "~". URI fragments are not accepted.
// The upstream hint language additionally treats a component "-" as a wildcard.
func ParseMapPath(pointer string) ([]string, error) {
	if pointer == "" {
		return []string{}, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("map path %q must be an empty pointer or begin with '/'", pointer)
	}
	parts := strings.Split(pointer[1:], "/")
	if len(parts) > MaxDepth {
		return nil, fmt.Errorf("map path exceeds maximum depth of %d", MaxDepth)
	}
	for i, part := range parts {
		var decoded strings.Builder
		for j := 0; j < len(part); j++ {
			if part[j] != '~' {
				decoded.WriteByte(part[j])
				continue
			}
			j++
			if j == len(part) || part[j] != '0' && part[j] != '1' {
				return nil, fmt.Errorf("map path %q contains an invalid '~' escape", pointer)
			}
			if part[j] == '0' {
				decoded.WriteByte('~')
			} else {
				decoded.WriteByte('/')
			}
		}
		parts[i] = decoded.String()
	}
	return parts, nil
}
