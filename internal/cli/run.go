package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/teyfix/bloblang-lsp/internal/mapping"
)

// Kept injectable for command tests; production always uses the subprocess
// runner, which enforces a hard execution deadline.
var executeMapping = mapping.Execute

type importFiles []string

func (i *importFiles) String() string { return strings.Join(*i, ", ") }
func (i *importFiles) Set(value string) error {
	if name, path, ok := strings.Cut(value, "="); !ok || name == "" || path == "" {
		return errors.New("--import requires name=path")
	}
	*i = append(*i, value)
	return nil
}

func runMappingCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var req mapping.Request
	var input, metadata, env, imports, root string
	var files importFiles
	var timeout time.Duration
	fs.StringVar(&req.Source, "source", "", "Inline mapping source (instead of a mapping path)")
	fs.StringVar(&input, "input", "", "Input JSON value, including null")
	fs.StringVar(&req.InputPath, "input-file", "", "Input JSON/YAML file; '-' reads JSON from stdin")
	fs.StringVar(&req.SamplePath, "sample", "", "Sample JSON/YAML file with a $bloblang envelope")
	fs.StringVar(&metadata, "metadata", "", "Initial metadata as a JSON object")
	fs.StringVar(&env, "env", "", "Environment fixtures as a JSON object of strings")
	fs.StringVar(&imports, "imports", "", "Import names mapped to source strings as a JSON object")
	fs.Var(&files, "import", "Import source file as name=path (repeatable)")
	fs.StringVar(&root, "root", "", "Optional initial output document as JSON")
	fs.DurationVar(&timeout, "timeout", mapping.DefaultTimeout, "Execution deadline, up to 5s")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return printMappingHelp(fs, stdout, stderr, "Usage: blobl run [options] [mapping.blobl|-]\nWith a mapping path, sample directives, sibling .sample.json/.yaml files and relative imports work as in the LSP.\nInput is required, either explicitly or from a sample. Host I/O functions are disabled; inline source requires explicit imports.\nJSON output contains output, metadata, deleted, optional rawOutput and structured error.\n")
		}
		return fail(stderr, err)
	}
	if timeout <= 0 || timeout > mapping.DefaultTimeout {
		return fail(stderr, errors.New("--timeout must be greater than zero and at most 5s"))
	}
	provided := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	if fs.NArg() > 1 || provided["source"] && fs.NArg() != 0 {
		return fail(stderr, errors.New("provide one mapping path or --source"))
	}
	if !provided["source"] && fs.NArg() == 0 {
		return fail(stderr, errors.New("a mapping path or --source is required"))
	}
	if provided["input"] {
		req.Input = json.RawMessage(input)
		if !json.Valid(req.Input) {
			return fail(stderr, errors.New("--input must be a JSON value; use null for null input"))
		}
	}
	if provided["root"] {
		req.Root = json.RawMessage(root)
		if !json.Valid(req.Root) {
			return fail(stderr, errors.New("--root must be a JSON value"))
		}
	}
	for _, field := range []struct {
		name, raw string
		target    any
	}{
		{"metadata", metadata, &req.Metadata},
		{"env", env, &req.Env},
		{"imports", imports, &req.Imports},
	} {
		if provided[field.name] {
			if err := decodeMappingObject(field.raw, field.target); err != nil {
				return fail(stderr, fmt.Errorf("--%s: %w", field.name, err))
			}
		}
	}
	if req.InputPath == "-" {
		if provided["input"] || req.SamplePath != "" {
			return fail(stderr, errors.New("--input, --input-file and --sample are mutually exclusive"))
		}
		if fs.NArg() == 1 && fs.Arg(0) == "-" {
			return fail(stderr, errors.New("stdin cannot supply both mapping source and input"))
		}
		data, err := readMappingData(stdin)
		if err != nil {
			return fail(stderr, err)
		}
		if !json.Valid(data) {
			return fail(stderr, errors.New("stdin input must contain one JSON value"))
		}
		req.Input, req.InputPath = data, ""
	}
	if fs.NArg() == 1 {
		if fs.Arg(0) == "-" {
			data, err := readMappingData(stdin)
			if err != nil {
				return fail(stderr, err)
			}
			req.Source = string(data)
		} else {
			req.MappingPath = fs.Arg(0)
		}
	}
	for _, item := range files {
		name, path, _ := strings.Cut(item, "=")
		if _, exists := req.Imports[name]; exists {
			return fail(stderr, fmt.Errorf("duplicate import %q", name))
		}
		data, err := readMappingFile(path)
		if err != nil {
			return fail(stderr, err)
		}
		if req.Imports == nil {
			req.Imports = map[string]string{}
		}
		req.Imports[name] = string(data)
	}
	resolved, err := mapping.Resolve(req)
	if err != nil {
		return fail(stderr, err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result := executeMapping(ctx, resolved)
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return fail(stderr, err)
	}
	if result.Error != nil {
		return 1
	}
	return 0
}

func runTestCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var format string
	var timeout time.Duration
	fs.StringVar(&format, "format", "text", "Results format: text or json")
	fs.DurationVar(&timeout, "timeout", mapping.DefaultTimeout, "Per-case execution deadline, up to 5s")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return printMappingHelp(fs, stdout, stderr, "Usage: blobl test [options] suite.json|suite.yaml|-\nRun named mapping fixtures with output, metadata, deletion and error expectations.\nOptions must precede the fixture suite path.\n")
		}
		return fail(stderr, err)
	}
	if fs.NArg() != 1 {
		return fail(stderr, errors.New("test requires one fixture suite path or '-'"))
	}
	if format != "text" && format != "json" {
		return fail(stderr, errors.New("--format must be text or json"))
	}
	if timeout <= 0 || timeout > mapping.DefaultTimeout {
		return fail(stderr, errors.New("--timeout must be greater than zero and at most 5s"))
	}
	var data []byte
	var err error
	if fs.Arg(0) == "-" {
		data, err = readMappingData(stdin)
	} else {
		data, err = readMappingFile(fs.Arg(0))
	}
	if err != nil {
		return fail(stderr, err)
	}
	suite, err := mapping.ParseSuite(data)
	if err != nil {
		return fail(stderr, err)
	}
	results, err := mapping.RunSuite(ctx, suite, func(ctx context.Context, req mapping.Request) mapping.Result {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return executeMapping(ctx, req)
	})
	if err != nil {
		return fail(stderr, err)
	}
	if format == "json" {
		if err := json.NewEncoder(stdout).Encode(results); err != nil {
			return fail(stderr, err)
		}
	} else {
		for _, test := range results.Cases {
			status := "PASS"
			if !test.Passed {
				status = "FAIL"
			}
			if _, err := fmt.Fprintf(stdout, "%s %s\n", status, test.Name); err != nil {
				return fail(stderr, err)
			}
			for _, failure := range test.Failures {
				if _, err := fmt.Fprintf(stdout, "  %s\n", failure); err != nil {
					return fail(stderr, err)
				}
			}
		}
		if _, err := fmt.Fprintf(stdout, "%d passed, %d failed\n", results.Passed, results.Failed); err != nil {
			return fail(stderr, err)
		}
	}
	if results.Failed > 0 {
		return 1
	}
	return 0
}

func printMappingHelp(fs *flag.FlagSet, stdout, stderr io.Writer, intro string) int {
	var help bytes.Buffer
	help.WriteString(intro)
	fs.SetOutput(&help)
	fs.PrintDefaults()
	if _, err := io.Copy(stdout, &help); err != nil {
		return fail(stderr, err)
	}
	return 0
}

func decodeMappingObject(raw string, target any) error {
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return errors.New("expected a JSON object")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("expected exactly one JSON object")
	}
	return nil
}

func readMappingFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := readMappingData(file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return data, nil
}

func readMappingData(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, mapping.MaxRequestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > mapping.MaxRequestBytes {
		return nil, errors.New("mapping input exceeds 4 MiB")
	}
	return data, nil
}
