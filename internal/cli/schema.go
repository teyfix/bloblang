package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/teyfix/bloblang/internal/mapping"
	"github.com/teyfix/bloblang/internal/schema"
	"go.yaml.in/yaml/v3"
)

func runSchemaCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var inputFile, sampleFile, outputFormat string
	var opts schema.Options
	fs := flag.NewFlagSet("schema", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&inputFile, "input-file", "", "Raw JSON or YAML sample file")
	fs.StringVar(&sampleFile, "sample", "", "LSP sample file containing a $bloblang envelope; summarizes its input")
	fs.StringVar(&outputFormat, "format", "json", "Output encoding: json or yaml (schema and sample metadata)")
	fs.Func("map-path", "JSON Pointer to an object with dynamic keys; repeatable (~1 escapes /, ~0 escapes ~)", func(value string) error {
		path, err := schema.ParseMapPath(value)
		if err != nil {
			return err
		}
		opts.MapPaths = append(opts.MapPaths, path)
		return nil
	})
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		var help bytes.Buffer
		fmt.Fprintln(&help, "Usage: blobl schema [--input-file FILE | --sample FILE] [--map-path POINTER] [--format json|yaml]")
		fmt.Fprintln(&help, "No file reads one raw JSON or YAML value from stdin.")
		fmt.Fprintln(&help, "Returns sample-based JSON Type Definition (JTD), with enums disabled and numeric types widened.")
		fmt.Fprintln(&help, "The result contains schema and meta; inferred fields are observations, not an authoritative contract.")
		fs.SetOutput(&help)
		fs.PrintDefaults()
		if _, err := io.Copy(stdout, &help); err != nil {
			return fail(stderr, err)
		}
		return 0
	} else if err != nil {
		return fail(stderr, err)
	}
	if fs.NArg() > 0 {
		return fail(stderr, errors.New("schema accepts no positional arguments; use --input-file or --sample"))
	}
	if inputFile != "" && sampleFile != "" {
		return fail(stderr, errors.New("--input-file and --sample are mutually exclusive"))
	}
	if outputFormat != "json" && outputFormat != "yaml" {
		return fail(stderr, errors.New("--format must be json or yaml"))
	}
	if err := ctx.Err(); err != nil {
		return fail(stderr, err)
	}
	req := mapping.Request{Source: "root = this", InputPath: inputFile, SamplePath: sampleFile}
	if inputFile == "" && sampleFile == "" {
		input, err := readSchemaStdin(stdin)
		if err != nil {
			return fail(stderr, err)
		}
		req.Input = input
	}
	resolved, err := mapping.Resolve(req)
	if err != nil {
		return fail(stderr, err)
	}
	var value any
	if err := json.Unmarshal(resolved.Input, &value); err != nil {
		return fail(stderr, fmt.Errorf("decode schema input: %w", err))
	}
	result, err := schema.Infer(value, opts)
	if err != nil {
		return fail(stderr, err)
	}
	if err := ctx.Err(); err != nil {
		return fail(stderr, err)
	}
	if outputFormat == "json" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(result)
	} else {
		// Normalize RawMessage into a YAML object instead of a byte sequence.
		var data []byte
		data, err = json.Marshal(result)
		if err == nil {
			var document any
			err = json.Unmarshal(data, &document)
			if err == nil {
				encoder := yaml.NewEncoder(stdout)
				encoder.SetIndent(2)
				err = encoder.Encode(document)
				if closeErr := encoder.Close(); err == nil {
					err = closeErr
				}
			}
		}
	}
	if err != nil {
		return fail(stderr, err)
	}
	return 0
}

func readSchemaStdin(input io.Reader) (json.RawMessage, error) {
	data, err := io.ReadAll(io.LimitReader(input, schema.MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > schema.MaxBytes {
		return nil, fmt.Errorf("input exceeds maximum size of %d bytes", schema.MaxBytes)
	}
	if json.Valid(data) {
		return json.RawMessage(data), nil
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode JSON or YAML input: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("expected exactly one JSON or YAML input value")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("input must be JSON-compatible: %w", err)
	}
	return encoded, nil
}
