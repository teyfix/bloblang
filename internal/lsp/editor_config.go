package lsp

import (
	"strconv"
	"strings"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/teyfix/bloblang/internal/editorconfig"
)

// Import AST nodes retain quoted source spelling. Match Benthos's string
// decoding before resolving paths, especially escaped Windows separators.
func decodeImportPath(literal string) string {
	if len(literal) >= 6 && strings.HasPrefix(literal, `"""`) && strings.HasSuffix(literal, `"""`) {
		return literal[3 : len(literal)-3]
	}
	if path, err := strconv.Unquote(literal); err == nil {
		return path
	}
	return strings.Trim(literal, "\"`")
}

func (h *Handler) sourceContext(uri protocol.DocumentURI) editorconfig.SourceContext {
	roots := append([]string(nil), h.workspaceRoots...)
	if h.workspaceRoot != "" {
		roots = append(roots, h.workspaceRoot)
	}
	return editorconfig.ResolveSource(h.baseDirForURI(uri), roots)
}

func (h *Handler) resolveImportPath(uri protocol.DocumentURI, path string) string {
	return (editorconfig.SourceContext{Dir: h.baseDirForURI(uri)}).ImportPath(path)
}

// Read on demand, so edits, creations and deletions take effect without restarting.
func (h *Handler) workspaceConfig(uri protocol.DocumentURI) editorconfig.Config {
	c, err := h.sourceContext(uri).Config()
	if err != nil && h.logger != nil {
		h.logger.Debug("invalid workspace configuration", "error", err)
	}
	return c
}
