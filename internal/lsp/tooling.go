package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/redpanda-data/benthos/v4/public/bloblang"
	"github.com/teyfix/bloblang-lsp/internal/benthos"
	"github.com/teyfix/bloblang-lsp/internal/editorconfig"
	"go.yaml.in/yaml/v3"
)

// Tooling provides synchronous formatting and static linting without opening an
// LSP document, reading samples, or executing Bloblang functions and methods.
// A Tooling instance may be reused for multiple files. Calls are serialized.
type Tooling struct {
	mu sync.Mutex
	h  *Handler
}

// ToolDiagnostic uses one-based lines and Unicode character columns.
type ToolDiagnostic struct {
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Severity string `json:"severity"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message"`
}

type LintResult struct {
	Text        string
	Diagnostics []ToolDiagnostic
}

func NewTooling() (*Tooling, error) {
	parser, err := benthos.NewBloblang()
	if err != nil {
		return nil, err
	}
	return &Tooling{h: &Handler{parser: parser, benv: benthos.NewEnvironment().Deactivated()}}, nil
}

// Close releases retained syntax trees. It is safe to call more than once.
func (t *Tooling) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.h != nil {
		t.h.parser.Close("")
		t.h = nil
	}
}

// operationConfig and finishOperation run under mu. The temporary project root
// lets YAML path checks use the same source context as the editor without
// leaking one file's project settings into the next operation.
func (t *Tooling) operationConfig(uri protocol.DocumentURI) (editorconfig.Config, error) {
	source := editorconfig.ResolveSource(t.h.baseDirForURI(uri), nil)
	t.h.workspaceRoot = source.Root
	return source.Config()
}

func (t *Tooling) finishOperation() {
	t.h.parser.Close("")
	t.h.workspaceRoot = ""
}

func toolingURI(path string) (protocol.DocumentURI, error) {
	if path == "" {
		return "", errors.New("a filename is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return fileURI(absolute), nil
}

// toolingYAML checks the entire stream: the editor region extractor supports a
// single YAML document, so accepting a second would silently miss its mappings.
func toolingYAML(text string) error {
	decoder := yaml.NewDecoder(strings.NewReader(text))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil && err != io.EOF {
		return err
	}
	if err := decoder.Decode(&document); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("multiple YAML documents are not supported")
	}
	return nil
}

func (t *Tooling) environment(uri protocol.DocumentURI) *bloblang.Environment {
	source := editorconfig.SourceContext{Dir: t.h.baseDirForURI(uri)}
	return t.h.benv.WithCustomImporter(func(name string) ([]byte, error) {
		return os.ReadFile(source.ImportPath(name))
	})
}

func (t *Tooling) staticTextDiagnostics(uri protocol.DocumentURI, text string) []protocol.Diagnostic {
	// Benthos treats comment-only text as an empty expression. The editor accepts
	// it as an empty mapping, which is also useful for CLI files and stdin.
	if !t.h.hasMappingCode(uri, text) {
		return nil
	}
	if _, err := t.environment(uri).Parse(text); err != nil {
		return convertErrorToDiagnostics(text, err)
	}
	return nil
}

func (t *Tooling) staticDiagnostics(uri protocol.DocumentURI, text string) []protocol.Diagnostic {
	if !yamlDocument(uri) {
		return t.staticTextDiagnostics(uri, text)
	}
	if err := toolingYAML(text); err != nil {
		severity := protocol.SeverityError
		return []protocol.Diagnostic{{Severity: &severity, Source: "yaml", Message: err.Error()}}
	}
	diagnostics := t.h.yamlPathDiagnostics(uri, text)
	for _, region := range extractRegions(uri, text) {
		for _, diagnostic := range t.staticTextDiagnostics(region.uri, region.text) {
			diagnostic.Range = region.hostRange(text, diagnostic.Range)
			diagnostics = append(diagnostics, diagnostic)
		}
	}
	return diagnostics
}

// Format preserves the surrounding YAML host and formats its embedded mappings.
// Invalid input and failed formatter safety checks return an error.
func (t *Tooling) Format(path, text string, tabSize int) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.h == nil {
		return "", errors.New("tooling is closed")
	}
	defer t.finishOperation()
	uri, err := toolingURI(path)
	if err != nil {
		return "", err
	}
	cfg, err := t.operationConfig(uri)
	if err != nil {
		return "", err
	}
	if yamlDocument(uri) {
		if err := toolingYAML(text); err != nil {
			return "", fmt.Errorf("cannot format: %w", err)
		}
	}
	valid := true
	edits := formatDocument(uri, text, tabSize, func(local protocol.DocumentURI, source string, indent int) (string, bool) {
		formatted, ok := t.h.formatTextWithEnvironment(local, source, indent, t.environment(local), cfg.Formatter.PrintWidth)
		valid = valid && ok
		return formatted, ok
	})
	if !valid {
		return "", errors.New("cannot format: formatter safety check failed")
	}
	formatted := applyToolEdits(text, edits)
	if diagnostics := t.staticDiagnostics(uri, formatted); len(diagnostics) > 0 {
		return "", fmt.Errorf("cannot format: %s", diagnostics[0].Message)
	}
	return formatted, nil
}

func (t *Tooling) findings(uri protocol.DocumentURI, text string, cfg editorconfig.Config) ([]protocol.Diagnostic, []protocol.TextEdit) {
	diagnostics := t.staticDiagnostics(uri, text)
	var edits []protocol.TextEdit
	appendFindings := func(local protocol.DocumentURI, source string, region *embeddedRegion) {
		for _, finding := range t.h.lintFindingsWithConfig(local, source, cfg) {
			diagnostic := finding.diagnostic
			if region != nil {
				diagnostic.Range = region.hostRange(text, diagnostic.Range)
			}
			diagnostics = append(diagnostics, diagnostic)
			// Match the editor's safe quick-fix restrictions: escaped/folded
			// scalars need reconstruction, and advisories have no replacement.
			if finding.replacement != "" && (region == nil || region.style == 0 || region.style&yaml.LiteralStyle != 0) {
				edit := protocol.TextEdit{Range: diagnostic.Range, NewText: finding.replacement}
				valid := true
				if region != nil {
					edit, valid = region.sourceEdit(text, finding.diagnostic.Range, finding.replacement)
				}
				if valid {
					edits = append(edits, edit)
				}
			}
		}
	}
	if yamlDocument(uri) {
		if toolingYAML(text) == nil {
			for _, region := range extractRegions(uri, text) {
				appendFindings(region.uri, region.text, &region)
			}
		}
	} else {
		appendFindings(uri, text, nil)
	}
	return diagnostics, edits
}

// applyToolEdits favors the outermost fix when ranges overlap, then applies
// disjoint edits from the end so their original offsets remain valid.
func applyToolEdits(text string, edits []protocol.TextEdit) string {
	sort.SliceStable(edits, func(i, j int) bool {
		startI, startJ := positionByte(text, edits[i].Range.Start), positionByte(text, edits[j].Range.Start)
		if startI != startJ {
			return startI < startJ
		}
		return positionByte(text, edits[i].Range.End) > positionByte(text, edits[j].Range.End)
	})
	selected := make([]protocol.TextEdit, 0, len(edits))
	end := -1
	for _, edit := range edits {
		start := positionByte(text, edit.Range.Start)
		if start < end {
			continue
		}
		selected = append(selected, edit)
		end = positionByte(text, edit.Range.End)
	}
	for i := len(selected) - 1; i >= 0; i-- {
		edit := selected[i]
		start, end := positionByte(text, edit.Range.Start), positionByte(text, edit.Range.End)
		text = text[:start] + edit.NewText + text[end:]
	}
	return text
}

// Lint performs static validation and configured lint rules. Safe fixes are
// revalidated and repeated to convergence before diagnostics are recomputed.
func (t *Tooling) Lint(path, text string, fix bool) (LintResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.h == nil {
		return LintResult{}, errors.New("tooling is closed")
	}
	defer t.finishOperation()
	uri, err := toolingURI(path)
	if err != nil {
		return LintResult{}, err
	}
	cfg, err := t.operationConfig(uri)
	if err != nil {
		return LintResult{}, err
	}
	if fix && len(t.staticDiagnostics(uri, text)) == 0 {
		seen := map[string]bool{text: true}
		for {
			_, edits := t.findings(uri, text, cfg)
			candidate := applyToolEdits(text, edits)
			if candidate == text || len(t.staticDiagnostics(uri, candidate)) != 0 {
				break
			}
			if seen[candidate] {
				return LintResult{}, errors.New("safe fixes did not converge")
			}
			seen[candidate] = true
			text = candidate
		}
	}
	diagnostics, _ := t.findings(uri, text, cfg)
	result := LintResult{Text: text, Diagnostics: make([]ToolDiagnostic, 0, len(diagnostics))}
	for _, diagnostic := range diagnostics {
		severity := "error"
		if diagnostic.Severity != nil {
			severity = map[protocol.DiagnosticSeverity]string{protocol.SeverityError: "error", protocol.SeverityWarning: "warn", protocol.SeverityInformation: "info", protocol.SeverityHint: "hint"}[*diagnostic.Severity]
		}
		var code string
		_ = json.Unmarshal(diagnostic.Code, &code)
		start := positionByte(text, diagnostic.Range.Start)
		lineStart := strings.LastIndex(text[:start], "\n") + 1
		result.Diagnostics = append(result.Diagnostics, ToolDiagnostic{Line: diagnostic.Range.Start.Line + 1, Column: utf8.RuneCountInString(text[lineStart:start]) + 1, Severity: severity, Code: code, Message: diagnostic.Message})
	}
	sort.SliceStable(result.Diagnostics, func(i, j int) bool {
		a, b := result.Diagnostics[i], result.Diagnostics[j]
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})
	return result, nil
}
