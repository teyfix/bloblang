package testutil

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/owenrumney/go-lsp/servertest"
	"github.com/teyfix/bloblang/internal/config"
	lsppkg "github.com/teyfix/bloblang/internal/lsp"
)

type Harness struct {
	t      *testing.T
	Server *servertest.Harness
}

func NewHarness(t *testing.T) *Harness {
	t.Helper()
	cfg := &config.Config{
		BloblangDocsURL:        "https://docs.redpanda.com/redpanda-connect/guides/bloblang",
		LogLevel:               "error",
		DiagnosticsDebounce:    20 * time.Millisecond,
		MaxInlineDocumentBytes: 150000,
		MaxInlineResultBytes:   100,
		PartialExecCacheSize:   100,
		PartialExecCacheTTL:    time.Minute,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	handler, err := lsppkg.NewHandler(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	return &Harness{t: t, Server: servertest.New(t, handler)}
}

func (h *Harness) OpenDocument(uri, text string) {
	h.t.Helper()
	if err := h.Server.DidOpen(protocol.DocumentURI(uri), "bloblang", text); err != nil {
		h.t.Fatalf("open document: %v", err)
	}
}

func (h *Harness) ChangeDocument(uri, text string) {
	h.t.Helper()
	if err := h.Server.DidChange(protocol.DocumentURI(uri), h.nextVersion(protocol.DocumentURI(uri)), text); err != nil {
		h.t.Fatalf("change document: %v", err)
	}
}

func (h *Harness) CloseDocument(uri string) {
	h.t.Helper()
	if err := h.Server.DidClose(protocol.DocumentURI(uri)); err != nil {
		h.t.Fatalf("close document: %v", err)
	}
}

func (h *Harness) GetCompletions(uri string, pos protocol.Position) []protocol.CompletionItem {
	h.t.Helper()
	list, err := h.Server.Completion(protocol.DocumentURI(uri), pos.Line, pos.Character)
	if err != nil {
		h.t.Fatalf("completion: %v", err)
	}
	return list.Items
}

func (h *Harness) GetHover(uri string, pos protocol.Position) *protocol.Hover {
	h.t.Helper()
	hover, err := h.Server.Hover(protocol.DocumentURI(uri), pos.Line, pos.Character)
	if err != nil {
		h.t.Fatalf("hover: %v", err)
	}
	return hover
}

func (h *Harness) GetInlayHints(uri string) []protocol.InlayHint {
	h.t.Helper()
	hints, err := h.Server.InlayHint(protocol.DocumentURI(uri), protocol.Range{
		Start: protocol.Position{Line: 0, Character: 0},
		End:   protocol.Position{Line: 1000, Character: 0},
	})
	if err != nil {
		h.t.Fatalf("inlay hints: %v", err)
	}
	return hints
}

func (h *Harness) GetCodeLenses(uri string) []protocol.CodeLens {
	h.t.Helper()
	lenses, err := h.Server.CodeLens(protocol.DocumentURI(uri))
	if err != nil {
		h.t.Fatalf("code lenses: %v", err)
	}
	return lenses
}

func (h *Harness) WaitForDiagnostics(uri string, timeout time.Duration) []protocol.Diagnostic {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	diags, err := h.Server.WaitForDiagnostics(ctx, protocol.DocumentURI(uri))
	if err != nil {
		h.t.Fatalf("wait diagnostics: %v", err)
	}
	return diags
}

func (h *Harness) nextVersion(uri protocol.DocumentURI) int {
	count := 1
	for _, diag := range h.Server.AllDiagnostics() {
		if diag.URI == uri {
			count++
		}
	}
	return count + int(time.Now().UnixNano()%100000)
}
