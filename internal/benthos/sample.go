package benthos

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Sample preserves input presence (including null) separately from its value.
type Sample struct {
	Root            any
	HasRoot         bool
	Value           any
	Meta            map[string]any
	Env             map[string]string
	Source          string
	Line            int
	Dependencies    []string
	DependencyLines map[string]int
}

type SampleError struct {
	Line    int
	Message string
}

func (e *SampleError) Error() string { return e.Message }

type sampleContext struct {
	sample   Sample
	hasInput bool
}
type DirectiveHandler func(*sampleContext, string, string) error

// The registry keeps directive parsing independent from data loading.
var directiveHandlers = map[string]DirectiveHandler{
	"root": func(c *sampleContext, raw, _ string) error {
		v, err := decodeSampleValue([]byte(raw))
		if err == nil {
			c.sample.Root = v
			c.sample.HasRoot = true
		}
		return err
	},
	"input": func(c *sampleContext, raw, _ string) error {
		v, err := decodeSampleValue([]byte(raw))
		if err == nil {
			c.sample.Value = v
			c.hasInput = true
		}
		return err
	},
	"meta": func(c *sampleContext, raw, _ string) error {
		v, err := decodeSampleValue([]byte(raw))
		if err != nil {
			return err
		}
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("meta must be an object")
		}
		c.sample.Meta = m
		return nil
	},
	"sample": func(c *sampleContext, raw, _ string) error {
		v, err := decodeSampleValue([]byte(raw))
		if err != nil {
			return err
		}
		return applySample(c, v, "sample")
	},
}

func decodeSampleValue(data []byte) (any, error) {
	var v any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&v) == nil {
		var trailing any
		if decoder.Decode(&trailing) == io.EOF {
			return normalizeNumbers(v), nil
		}
	}
	// JSON-looking YAML can contain comments or document markers after a value.
	// Retry the entire input, but require exactly one complete YAML document too.
	v = nil
	yamlDecoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := yamlDecoder.Decode(&v); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("sample must contain one JSON or YAML document")
		}
		return nil, err
	}
	var trailing any
	if err := yamlDecoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("invalid trailing sample content: %w", err)
		}
		return nil, fmt.Errorf("sample must contain only one JSON or YAML document")
	}
	return v, nil
}
func applySample(c *sampleContext, value any, field string) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("sample must be an object with input and optional meta")
	}
	if field == "sample" || field == "root" {
		if v, exists := obj["root"]; exists {
			c.sample.Root = v
			c.sample.HasRoot = true
		} else if field == "root" {
			return fmt.Errorf("sample is missing root")
		}
	}
	if field == "sample" || field == "input" {
		v, exists := obj["input"]
		if !exists {
			return fmt.Errorf("sample is missing input")
		}
		c.sample.Value = v
		c.hasInput = true
	}
	if field == "sample" {
		c.sample.Env = nil
		if raw, exists := obj["env"]; exists {
			values, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("sample env must be an object of string values")
			}
			c.sample.Env = make(map[string]string, len(values))
			for name, value := range values {
				s, ok := value.(string)
				if !ok {
					return fmt.Errorf("sample env value for %q must be a string", name)
				}
				c.sample.Env[name] = s
			}
		}
	}
	if field == "sample" || field == "meta" {
		if v, exists := obj["meta"]; exists {
			m, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("sample meta must be an object")
			}
			c.sample.Meta = m
		} else if field == "meta" {
			return fmt.Errorf("sample is missing meta")
		} else {
			c.sample.Meta = nil
		}
	}
	return nil
}

const MaxSampleFileBytes = 4 << 20

// ReadSampleFile rejects devices, pipes and oversized fixtures before opening
// them. Recheck the opened file and cap the read in case a regular file grows.
func ReadSampleFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	check := func(info os.FileInfo) error {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: sample must be a regular file", path)
		}
		if info.Size() > MaxSampleFileBytes {
			return fmt.Errorf("%s: sample exceeds %d-byte limit", path, MaxSampleFileBytes)
		}
		return nil
	}
	if err := check(info); err != nil {
		return nil, err
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
	if err := check(info); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxSampleFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxSampleFileBytes {
		return nil, fmt.Errorf("%s: sample exceeds %d-byte limit", path, MaxSampleFileBytes)
	}
	return data, nil
}

func loadSample(c *sampleContext, path, field string) error {
	data, err := ReadSampleFile(path)
	if err != nil {
		return err
	}
	v, err := decodeSampleValue(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: missing mandatory $bloblang envelope", path)
	}
	env, exists := obj["$bloblang"]
	if !exists {
		return fmt.Errorf("%s: missing mandatory $bloblang envelope", path)
	}
	if err = applySample(c, env, field); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	c.sample.Source = path
	c.sample.Dependencies = append(c.sample.Dependencies, path)
	return nil
}

func ExtractSample(parser *Bloblang, uri, text, baseDir string) (*Sample, error) {
	return ExtractSampleWithSource(parser, uri, text, baseDir, "", "")
}

// ExtractSampleWithSource selects an explicit YAML source or numbered sibling before the shared sibling.
func ExtractSampleWithSource(_ *Bloblang, uri, text, baseDir, explicitPath, numberedStem string) (*Sample, error) {
	c := &sampleContext{sample: Sample{Source: "inline"}}
	u, _ := url.Parse(uri)
	stem := strings.TrimSuffix(filepath.Base(u.Path), filepath.Ext(u.Path))
	var siblings []string
	for _, ext := range []string{"json", "yaml", "yml"} {
		p := filepath.Join(baseDir, stem+".sample."+ext)
		if _, err := os.Stat(p); err == nil {
			siblings = append(siblings, p)
		}
	}
	if numberedStem != "" {
		var numbered []string
		for _, ext := range []string{"json", "yaml", "yml"} {
			p := filepath.Join(baseDir, numberedStem+"."+ext)
			if _, err := os.Stat(p); err == nil {
				numbered = append(numbered, p)
			}
		}
		if len(numbered) > 0 {
			siblings = numbered
		}
	}
	if explicitPath != "" {
		p := explicitPath
		if !filepath.IsAbs(p) {
			p = filepath.Join(baseDir, p)
		}
		siblings = []string{p}
	}
	lines := strings.Split(text, "\n")
	// Explicit entire-sample source resolves sibling ambiguity.
	explicit := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, "#") {
			break
		}
		if strings.HasPrefix(t, "#!sample_from ") || strings.HasPrefix(t, "#!sample ") {
			explicit = true
		}
	}
	if len(siblings) > 1 && !explicit {
		return nil, &SampleError{Message: "Multiple sibling samples found; select one with #!sample_from"}
	}
	if len(siblings) == 1 && !explicit {
		if err := loadSample(c, siblings[0], "sample"); err != nil {
			return nil, &SampleError{Message: err.Error()}
		}
	}
	seen := map[string]bool{}
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || (strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#!")) {
			continue
		}
		if !strings.HasPrefix(line, "#!") {
			break
		}
		parts := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(line, "#!")), " ", 2)
		name := parts[0]
		raw := ""
		if len(parts) > 1 {
			raw = strings.TrimSpace(parts[1])
		}
		start := i
		if raw == "|" {
			var body []string
			for i+1 < len(lines) {
				next := strings.TrimLeft(lines[i+1], " \t")
				if !strings.HasPrefix(next, "#|") {
					break
				}
				i++
				next = strings.TrimPrefix(next, "#|")
				if strings.HasPrefix(next, " ") {
					next = next[1:]
				}
				body = append(body, next)
			}
			raw = strings.Join(body, "\n")
		}
		field := strings.TrimSuffix(name, "_from")
		handler, ok := directiveHandlers[field]
		if !ok {
			return nil, &SampleError{Line: start, Message: fmt.Sprintf("Unknown sample directive %q", name)}
		}
		if seen[name] {
			return nil, &SampleError{Line: start, Message: "Duplicate #!" + name + " directive"}
		}
		seen[name] = true
		var err error
		if strings.TrimSpace(raw) == "" {
			return nil, &SampleError{Line: start, Message: "Missing value for #!" + name}
		}
		if strings.HasSuffix(name, "_from") {
			p := strings.Trim(raw, "\"'")
			if p == "" {
				err = fmt.Errorf("missing sample file path")
			} else {
				if !filepath.IsAbs(p) {
					p = filepath.Join(baseDir, p)
				}
				err = loadSample(c, p, field)
				if err == nil {
					if c.sample.DependencyLines == nil {
						c.sample.DependencyLines = map[string]int{}
					}
					c.sample.DependencyLines[p] = start
				}
			}
		} else {
			err = handler(c, raw, baseDir)
		}
		if err != nil {
			return nil, &SampleError{Line: start, Message: err.Error()}
		}
		c.sample.Line = start
	}
	if !c.hasInput {
		return nil, nil
	}
	return &c.sample, nil
}

func normalizeNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, e := strconv.ParseInt(string(x), 10, 64); e == nil {
			return i
		}
		// Keep decimal lexemes and integers outside int64 exact. Benthos accepts
		// json.Number and applies its numeric conversion only when an operation
		// needs it; loading fixtures must not round pass-through values.
		return x
	case map[string]any:
		for k, c := range x {
			x[k] = normalizeNumbers(c)
		}
	case []any:
		for i, c := range x {
			x[i] = normalizeNumbers(c)
		}
	}
	return v
}
