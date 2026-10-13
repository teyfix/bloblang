package lsp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"unicode/utf16"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/redpanda-data/benthos/v4/public/bloblang"
	"github.com/teyfix/bloblang-lsp/internal/benthos"
	"github.com/teyfix/bloblang-lsp/internal/fileuri"
)

func (h *Handler) validateDocument(ctx context.Context, uri protocol.DocumentURI, version uint64) {
	defer h.discardClosedDocumentTrees(uri)
	if ctx.Err() != nil {
		return
	}
	h.mu.Lock()
	text, ok := h.documents.Text(uri)
	current := h.latestVersion[uri]
	h.mu.Unlock()
	if !ok || current != version {
		return
	}

	diagnostics := append([]protocol.Diagnostic{}, h.sampleDiagnosticsFor(uri)...)
	if yamlDocument(uri) && !strings.Contains(string(uri), "#bloblang-") {
		diagnostics = h.yamlPathDiagnostics(uri, text)
		seenMissing := false
		for _, r := range h.regions(uri) {
			if ctx.Err() != nil {
				return
			}
			for _, d := range h.diagnosticsTextContext(ctx, r.uri, r.text) {
				if strings.Contains(d.Message, "not found; add one") {
					if seenMissing {
						continue
					}
					seenMissing = true
				}
				d.Range = r.hostRange(text, d.Range)
				diagnostics = append(diagnostics, d)
			}
		}
	} else {
		diagnostics = h.diagnosticsTextContext(ctx, uri, text)
	}

	h.mu.Lock()
	currentVersion, ok := h.latestVersion[uri]
	if !ok || version != currentVersion || ctx.Err() != nil || h.stopped {
		h.mu.Unlock()
		return // Stale validation run or document was closed!
	}

	h.publishDiagnostics(ctx, uri, diagnostics)
	h.mu.Unlock()
	if h.validationHook != nil {
		h.validationHook(uri, diagnostics)
	}
}

func indentMessage(msg string) string {
	// Clean up newlines first to get the raw single line format
	msg = strings.Split(msg, "\n")[0]
	parts := strings.Split(msg, ": ")
	var indentParts []string
	for i, part := range parts {
		indent := strings.Repeat("  ", i)
		if i < len(parts)-1 {
			indentParts = append(indentParts, indent+part+":")
		} else {
			indentParts = append(indentParts, indent+part)
		}
	}
	return strings.Join(indentParts, "\n")
}

func convertErrorToDiagnostics(text string, err error) []protocol.Diagnostic {
	severity := protocol.SeverityError
	source := "bloblang"

	rawErr := ""
	if prettyErr, ok := err.(*bloblang.ParseError); ok {
		rawErr = prettyErr.ErrorMultiline()
	} else {
		rawErr = err.Error()
	}

	var line, char int
	idx := strings.Index(rawErr, "line ")
	if idx == -1 {
		return []protocol.Diagnostic{{
			Range:    protocol.Range{Start: protocol.Position{Line: 0, Character: 0}, End: protocol.Position{Line: 0, Character: 0}},
			Severity: &severity,
			Message:  rawErr,
			Source:   source,
		}}
	}

	if _, scanErr := fmt.Sscanf(rawErr[idx:], "line %d char %d", &line, &char); scanErr != nil {
		return []protocol.Diagnostic{{
			Range:    protocol.Range{Start: protocol.Position{Line: 0, Character: 0}, End: protocol.Position{Line: 0, Character: 0}},
			Severity: &severity,
			Message:  scanErr.Error(),
			Source:   source,
		}}
	}

	cleanMessage := rawErr
	if parts := strings.SplitN(rawErr, ": ", 2); len(parts) == 2 {
		cleanMessage = parts[1]
	}
	if parts := strings.SplitN(cleanMessage, ": ", 2); strings.HasPrefix(cleanMessage, "line ") && len(parts) == 2 {
		cleanMessage = parts[1]
	}
	cleanMessage = strings.Split(cleanMessage, "\n")[0]

	if line > 0 {
		line--
	}
	if char > 0 {
		char--
	}

	lines := strings.Split(text, "\n")
	if line >= 0 && line < len(lines) {
		runes := []rune(lines[line])
		if char <= len(runes) {
			char = len(utf16.Encode(runes[:char]))
		}
	}
	return []protocol.Diagnostic{{
		Range: protocol.Range{
			Start: protocol.Position{Line: line, Character: char},
			End:   protocol.Position{Line: line, Character: char},
		},
		Severity: &severity,
		Message:  cleanMessage,
		Source:   source,
	}}
}

func (h *Handler) publishDiagnostics(ctx context.Context, uri protocol.DocumentURI, diagnostics []protocol.Diagnostic) {
	if diagnostics == nil {
		diagnostics = []protocol.Diagnostic{}
	}
	h.lastDiagnosticsMu.Lock()
	h.lastDiagnostics[uri] = append([]protocol.Diagnostic(nil), diagnostics...)
	h.lastDiagnosticsMu.Unlock()

	h.execDiagnosticsMu.RLock()
	exec := append([]protocol.Diagnostic(nil), h.execDiagnostics[uri]...)
	h.execDiagnosticsMu.RUnlock()

	merged := mergeDiagnostics(diagnostics, exec)

	if h.client == nil {
		return
	}
	if err := h.client.PublishDiagnostics(ctx, &protocol.PublishDiagnosticsParams{URI: uri, Diagnostics: merged}); err != nil {
		h.logger.Debug("publish diagnostics failed", "uri", uri, "err", err)
	}
}

func uriToPath(uri protocol.DocumentURI) (string, error) {
	return fileuri.Path(string(uri))
}

func (h *Handler) importHintDiagnostics(uri protocol.DocumentURI, text string) []protocol.Diagnostic {
	severity := protocol.SeverityInformation
	source := "bloblang"
	var diagnostics []protocol.Diagnostic

	tree, parseErr := h.parser.Parse(string(uri), text)
	if parseErr == nil && tree != nil {
		defer tree.Close()
		root := tree.RootNode()
		for i := uint(0); i < root.ChildCount(); i++ {
			child := root.Child(i)
			if child.Kind() == "import_statement" {
				var pathStr string
				for j := uint(0); j < child.ChildCount(); j++ {
					gc := child.Child(j)
					if gc.Kind() == "string" {
						pathStr = gc.Utf8Text([]byte(text))
						break
					}
				}
				if pathStr == "" {
					continue
				}
				pathStr = decodeImportPath(pathStr)
				resolved := h.resolveImportPath(uri, pathStr)
				row := int(child.StartPosition().Row)
				diagnostics = append(diagnostics, protocol.Diagnostic{
					Range:    protocol.Range{Start: protocol.Position{Line: row, Character: 0}, End: protocol.Position{Line: row, Character: 0}},
					Severity: &severity,
					Source:   source,
					Message:  fmt.Sprintf("Importing from %s", resolved),
				})
			}
		}
	}

	if strings.HasPrefix(string(uri), "untitled:") {
		diagnostics = append(diagnostics, protocol.Diagnostic{
			Range:    protocol.Range{Start: protocol.Position{Line: 0, Character: 0}, End: protocol.Position{Line: 0, Character: 0}},
			Severity: &severity,
			Source:   source,
			Message:  fmt.Sprintf("Import base directory: %s", h.baseDirForURI(uri)),
		})
	}
	return diagnostics
}

func (h *Handler) hasMappingCode(uri protocol.DocumentURI, text string) bool {
	tree, err := h.parser.Parse(string(uri)+":diagnostic-code", text)
	if err != nil || tree == nil {
		return true
	}
	defer tree.Close()
	for i := uint(0); i < tree.RootNode().NamedChildCount(); i++ {
		if tree.RootNode().NamedChild(i).Kind() != "comment" {
			return true
		}
	}
	return false
}

func (h *Handler) diagnosticsText(uri protocol.DocumentURI, text string) []protocol.Diagnostic {
	return h.diagnosticsTextContext(context.Background(), uri, text)
}

func (h *Handler) diagnosticsTextContext(ctx context.Context, uri protocol.DocumentURI, text string) []protocol.Diagnostic {
	diagnostics := []protocol.Diagnostic{}
	if ctx.Err() != nil {
		return diagnostics
	}
	if text != "" && h.hasMappingCode(uri, text) {
		preview, err := benthos.PreviewEnvironment(h.benv, h.getSample(uri))
		if err != nil {
			h.logger.Debug("preview environment setup failed", "err", err)
			preview = h.benv
		}
		env := preview.WithCustomImporter(func(name string) ([]byte, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return os.ReadFile(h.resolveImportPath(uri, name))
		})
		if _, err := env.Parse(text); err != nil {
			diagnostics = append(diagnostics, convertErrorToDiagnostics(text, err)...)
		} else if sample := h.getSample(uri); sample != nil && ctx.Err() == nil {
			if _, err := h.executor.ExecuteThroughContext(ctx, h.parser, string(uri), sample, text, uint(len(text))); err != nil && ctx.Err() == nil {
				severity := protocol.SeverityWarning
				diagnostics = append(diagnostics, protocol.Diagnostic{Range: protocol.Range{}, Severity: &severity, Source: "bloblang sample", Message: err.Error()})
			}
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	if strings.HasPrefix(string(uri), "untitled:") {
		diagnostics = append(diagnostics, h.importHintDiagnostics(uri, text)...)
	}
	diagnostics = append(diagnostics, h.sampleDiagnosticsFor(uri)...)
	diagnostics = append(diagnostics, h.lintDiagnostics(uri, text)...)
	return diagnostics
}

// LSP requires an array even when clearing diagnostics. A nil Go slice encodes
// as null, which vscode-languageclient rejects and leaves stale errors visible.
func mergeDiagnostics(base, execution []protocol.Diagnostic) []protocol.Diagnostic {
	merged := make([]protocol.Diagnostic, 0, len(base)+len(execution))
	merged = append(merged, base...)
	return append(merged, execution...)
}
