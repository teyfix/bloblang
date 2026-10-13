package lsp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/teyfix/bloblang/internal/benthos"
	"github.com/teyfix/bloblang/internal/config"
)

func largePreviewFixture() any {
	items := make([]any, 500)
	for i := range items {
		items[i] = map[string]any{"id": i, "name": "Preview sample", "overview": strings.Repeat("sample text ", 20), "nested": map[string]any{"active": true, "tags": []any{"one", "two", "three"}}}
	}
	return map[string]any{"items": items}
}

func BenchmarkLargePreview(b *testing.B) {
	cfg, err := config.Load()
	if err != nil {
		b.Fatal(err)
	}
	h, err := NewHandler(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		b.Fatal(err)
	}
	value := largePreviewFixture()
	raw, err := json.Marshal(value)
	if err != nil {
		b.Fatal(err)
	}
	uri := protocol.DocumentURI("file:///tmp/preview-benchmark.blobl")
	b.Logf("sample JSON: %d bytes", len(raw))
	b.Run("full_yaml", func(b *testing.B) {
		for b.Loop() {
			h.preview(uri, string(raw))
		}
	})
	b.Run("tooltip", func(b *testing.B) {
		for b.Loop() {
			h.previewMarkdown(uri, string(raw))
		}
	})
	text := "root = this"
	_, err = h.documents.Open(&protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{URI: uri, Text: text, LanguageID: "bloblang"}})
	if err != nil {
		b.Fatal(err)
	}
	h.samples[uri] = &benthos.Sample{Value: value}
	b.Run("hover", func(b *testing.B) {
		for b.Loop() {
			v, err := h.Hover(context.Background(), &protocol.HoverParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Position: protocol.Position{Character: 8}}})
			if err != nil || v == nil {
				b.Fatalf("hover: %v %v", v, err)
			}
		}
	})
}
