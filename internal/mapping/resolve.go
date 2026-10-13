package mapping

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/teyfix/bloblang/internal/benthos"
	"github.com/teyfix/bloblang/internal/fileuri"
	"go.yaml.in/yaml/v3"
)

// Resolve loads mapping and input files before a request enters the isolated
// evaluator. MappingPath and InputPath are relative to the working directory;
// SamplePath and mapping sample directives are relative to the mapping file.
// With inline Source, an explicit SamplePath is relative to the working
// directory, but automatic sibling discovery and relative sample directives
// are disabled. For MappingPath, transitive imports are snapshotted relative to
// that file; explicit Imports entries override filesystem content. The isolated
// evaluator itself continues to read only the resulting Imports map.
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
		req.Origin = path
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
	if req.MappingPath != "" {
		if err := snapshotImports(&req, baseDir); err != nil {
			return Request{}, err
		}
	}
	req.MappingPath, req.InputPath, req.SamplePath = "", "", ""
	if _, err := requestSize(req); err != nil {
		return Request{}, err
	}
	return req, nil
}

// Benthos resolves nested import names relative to the importing file before
// invoking this callback. Joining those names against the entrypoint directory
// matches the LSP importer. Its parser does not detect recursive import cycles,
// so bound callback invocations even for already-cached names.
func snapshotImports(req *Request, baseDir string) error {
	remaining, err := requestSize(*req)
	if err != nil {
		return err
	}
	remaining = MaxRequestBytes - remaining
	imports := make(map[string]string, len(req.Imports))
	for name, source := range req.Imports {
		imports[name] = source
	}
	const maxImportReads = 256
	reads := 0
	var importErr error
	env := benthos.NewEnvironment().Deactivated().WithCustomImporter(func(name string) ([]byte, error) {
		reads++
		if reads > maxImportReads {
			importErr = fmt.Errorf("import traversal exceeds %d reads (possible import cycle)", maxImportReads)
			return nil, importErr
		}
		if source, ok := imports[name]; ok {
			return []byte(source), nil
		}
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		data, err := readRequestFile(path)
		if err != nil {
			importErr = fmt.Errorf("import %q: %w", name, err)
			return nil, importErr
		}
		// Bound retained source bytes while walking. The exact serialized
		// request, including JSON escaping, is checked after traversal.
		remaining -= len(name) + len(data)
		if remaining < 0 {
			importErr = fmt.Errorf("mapping request including imports exceeds %d bytes", MaxRequestBytes)
			return nil, importErr
		}
		imports[name] = string(data)
		return data, nil
	})
	// Syntax/type errors belong to evaluation diagnostics. Only file loading
	// and traversal-limit errors prevent fixture resolution.
	_, _ = env.Parse(req.Source)
	if importErr != nil {
		return importErr
	}
	req.Imports = imports
	return nil
}

// SnapshotImports prepares imports for static lint/format requests that do not
// have execution input. Source is already loaded by the caller. A nonempty
// mappingPath provides the source's filesystem context; an empty path permits
// only supplied imports. Returned entries are owned by the caller and bounded
// together with source to the same request-size limit as mapping execution.
// Syntax errors are left to the caller's normal formatter/linter diagnostics.
func SnapshotImports(source, mappingPath string, supplied map[string]string) (map[string]string, error) {
	req := Request{Source: source, Imports: make(map[string]string, len(supplied))}
	for name, contents := range supplied {
		req.Imports[name] = contents
	}
	if mappingPath != "" {
		path, err := filepath.Abs(mappingPath)
		if err != nil {
			return nil, err
		}
		if err := snapshotImports(&req, filepath.Dir(path)); err != nil {
			return nil, err
		}
	}
	if _, err := requestSize(req); err != nil {
		return nil, err
	}
	return req.Imports, nil
}

func requestSize(req Request) (int, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return 0, fmt.Errorf("mapping request: %w", err)
	}
	if len(data) > MaxRequestBytes {
		return 0, fmt.Errorf("mapping request exceeds %d bytes", MaxRequestBytes)
	}
	return len(data), nil
}

func readRequestFile(path string) ([]byte, error) {
	// Stat before opening so pipes and devices cannot block request handling.
	// Recheck the opened descriptor in case the path changed in between.
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("file %q is not a regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("file %q is not a regular file", path)
	}
	if info.Size() > MaxRequestBytes {
		return nil, fmt.Errorf("file %q exceeds maximum request size of %d bytes", path, MaxRequestBytes)
	}
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
