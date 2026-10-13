package lsp

import (
	"context"
	protocol "github.com/owenrumney/go-lsp/lsp"
	"strings"
)

func (h *Handler) DidChangeWatchedFiles(ctx context.Context, _ *protocol.DidChangeWatchedFilesParams) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return nil
	}
	for _, uri := range h.documents.URIs() {
		if strings.Contains(string(uri), "#bloblang-") {
			continue
		}
		text, _ := h.documents.Text(uri)
		validation, version := h.beginDocumentVersion(uri)
		if yamlDocument(uri) {
			h.syncRegions(uri, text)
		} else {
			h.updateSample(uri, text)
		}
		h.executor.InvalidateDocument(string(uri))
		h.clearExecDiagnostics(uri)
		h.scheduleValidation(validation, uri, version)
	}
	return nil
}
