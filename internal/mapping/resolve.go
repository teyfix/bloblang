package mapping

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/teyfix/bloblang-lsp/internal/benthos"
	"github.com/teyfix/bloblang-lsp/internal/fileuri"
	"go.yaml.in/yaml/v3"
)

// Resolve loads mapping and input files before a request enters the isolated
// evaluator. MappingPath and InputPath are relative to the working directory;
// SamplePath and mapping sample directives are relative to the mapping file.
// With inline Source, an explicit SamplePath is relative to the working
// directory, but automatic sibling discovery and relative sample directives
// are disabled. Filesystem imports remain unavailable to the evaluator.
//
// An explicit input or sample path overrides all mapping sample directives.
// Otherwise sample discovery follows the editor's directives and
// .sample.json/.yaml/.yml sibling rules. Explicit Metadata, Env
// and Root replace sample fields; empty maps and JSON null count as explicit.
// Resolved requests retain source and fixtures only, with all path fields empty.
func Resolve(req Request) (Request, error) {
	if req.Source != "" && req.MappingPath != "" {
		return Request{}, fmt.Errorf("source and mappingPath are mutually exclusive")
	}
	if req.Source == "" && req.MappingPath == "" {
		return Request{}, fmt.Errorf("source or mappingPath is required")
	}
	inputs := 0
	for _, present := range []bool{len(req.Input) != 0, req.InputPath != "", req.SamplePath != ""} {
		if present {
			inputs++
		}
	}
	if inputs > 1 {
		return Request{}, fmt.Errorf("input, inputPath and samplePath are mutually exclusive")
	}

	// The shared extractor probes siblings unconditionally. A non-directory
	// base prevents inline source from accidentally discovering files in cwd.
	uri, baseDir := "untitled:bloblang-run", os.DevNull
	if req.MappingPath != "" {
		path, err := filepath.Abs(req.MappingPath)
		if err != nil {
			return Request{}, fmt.Errorf("mappingPath: %w", err)
		}
		data, err := readRequestFile(path)
		if err != nil {
			return Request{}, fmt.Errorf("mappingPath: %w", err)
		}
		req.Source = string(data)
		uri, baseDir = fileuri.FromPath(path), filepath.Dir(path)
	} else if req.SamplePath != "" {
		var err error
		baseDir, err = os.Getwd()
		if err != nil {
			return Request{}, fmt.Errorf("samplePath: %w", err)
		}
	}

	if req.InputPath != "" {
		path, err := filepath.Abs(req.InputPath)
		if err != nil {
			return Request{}, fmt.Errorf("inputPath: %w", err)
		}
		data, err := readRequestFile(path)
		if err != nil {
			return Request{}, fmt.Errorf("inputPath: %w", err)
		}
		if req.Input, err = decodeInputFile(data, filepath.Ext(path)); err != nil {
			return Request{}, fmt.Errorf("inputPath %q: %w", path, err)
		}
	} else if len(req.Input) == 0 {
		if req.MappingPath == "" && req.SamplePath == "" {
			if err := checkInlineSamplePaths(req.Source); err != nil {
				return Request{}, err
			}
		}
		var sample *benthos.Sample
		var err error
		if req.SamplePath != "" {
			sample, err = benthos.ExtractSampleWithSource(nil, uri, "", baseDir, req.SamplePath, "")
		} else {
			sample, err = benthos.ExtractSample(nil, uri, req.Source, baseDir)
		}
		if err != nil {
			return Request{}, fmt.Errorf("sample: %w", err)
		}
		if sample == nil {
			return Request{}, fmt.Errorf("no input supplied or sample found; provide input, inputPath, samplePath, or a mapping sample directive/sibling")
		}
		if req.Input, err = json.Marshal(sample.Value); err != nil {
			return Request{}, fmt.Errorf("sample input must be JSON-compatible: %w", err)
		}
		if req.Metadata == nil {
			req.Metadata = sample.Meta
		}
		if req.Env == nil {
			req.Env = sample.Env
		}
		if len(req.Root) == 0 && sample.HasRoot {
			if req.Root, err = json.Marshal(sample.Root); err != nil {
				return Request{}, fmt.Errorf("sample root must be JSON-compatible: %w", err)
			}
		}
	}
	req.MappingPath, req.InputPath, req.SamplePath = "", "", ""
	return req, nil
}

func readRequestFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxRequestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxRequestBytes {
		return nil, fmt.Errorf("file %q exceeds maximum request size of %d bytes", path, MaxRequestBytes)
	}
	return data, nil
}

func decodeInputFile(data []byte, ext string) (json.RawMessage, error) {
	if json.Valid(data) {
		return json.RawMessage(data), nil
	}
	if strings.EqualFold(ext, ".json") {
		_, err := decodeValue(data)
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("expected exactly one YAML document")
		}
		return nil, fmt.Errorf("decode YAML: %w", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("input must be JSON-compatible: %w", err)
	}
	return encoded, nil
}

func checkInlineSamplePaths(source string) error {
	for _, line := range strings.Split(source, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || (strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#!")) {
			continue
		}
		if !strings.HasPrefix(line, "#!") {
			break
		}
		name, raw, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(line, "#!")), " ")
		if strings.HasSuffix(name, "_from") && !filepath.IsAbs(strings.Trim(strings.TrimSpace(raw), "\"'")) {
			return fmt.Errorf("relative #!%s requires mappingPath or an explicit samplePath", name)
		}
	}
	return nil
}
