package lsp

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/redpanda-data/benthos/v4/public/bloblang"
	"github.com/stretchr/testify/require"
)

func TestEditorHandlersDiscardClosedDocumentTrees(t *testing.T) {
	handlers := map[string]func(*Handler, protocol.DocumentURI){
		"formatting": func(h *Handler, uri protocol.DocumentURI) {
			_, _ = h.Formatting(context.Background(), &protocol.DocumentFormattingParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}})
		},
		"code action": func(h *Handler, uri protocol.DocumentURI) {
			_, _ = h.CodeAction(context.Background(), &protocol.CodeActionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}})
		},
		"definition": func(h *Handler, uri protocol.DocumentURI) {
			_, _ = h.Definition(context.Background(), &protocol.DefinitionParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}})
		},
		"references": func(h *Handler, uri protocol.DocumentURI) {
			_, _ = h.References(context.Background(), &protocol.ReferenceParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}})
		},
		"prepare rename": func(h *Handler, uri protocol.DocumentURI) {
			_, _ = h.PrepareRename(context.Background(), &protocol.PrepareRenameParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}})
		},
		"rename": func(h *Handler, uri protocol.DocumentURI) {
			_, _ = h.Rename(context.Background(), &protocol.RenameParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, NewName: "newName"})
		},
		"signature": func(h *Handler, uri protocol.DocumentURI) {
			_, _ = h.SignatureHelp(context.Background(), &protocol.SignatureHelpParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}})
		},
		"symbols": func(h *Handler, uri protocol.DocumentURI) {
			_, _ = h.DocumentSymbol(context.Background(), &protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}})
		},
		"folding": func(h *Handler, uri protocol.DocumentURI) {
			_, _ = h.FoldingRange(context.Background(), &protocol.FoldingRangeParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}})
		},
	}
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			h, uri := runtimeHandler(t)
			// A request already holding text can repopulate these after DidClose.
			key := string(uri) + "#bloblang-0:format"
			before, err := h.parser.Parse(key, "root = this")
			require.NoError(t, err)
			defer before.Close()
			handler(h, uri)
			after, err := h.parser.Parse(key, "root = this")
			require.NoError(t, err)
			defer after.Close()
			require.NotEqual(t, before.RootNode().Id(), after.RootNode().Id(), "closed source must not retain cached trees")
		})
	}
}

func TestFormattingCannotRestoreTreesAfterClose(t *testing.T) {
	h, uri := runtimeHandler(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	require.NoError(t, h.benv.RegisterFunctionV2("format_gate", bloblang.NewPluginSpec().Impure(), func(*bloblang.ParsedParams) (bloblang.Function, error) {
		once.Do(func() { close(entered); <-release })
		return func() (any, error) { return nil, nil }, nil
	}))
	source := "root=format_gate()"
	openFeature(t, h, uri, source)
	done := make(chan error, 1)
	go func() {
		_, err := h.Formatting(context.Background(), &protocol.DocumentFormattingParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Options: protocol.FormattingOptions{TabSize: 2}})
		done <- err
	}()
	<-entered
	require.NoError(t, h.DidClose(context.Background(), &protocol.DidCloseTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}))
	before, err := h.parser.Parse(string(uri)+":format", source)
	require.NoError(t, err)
	defer before.Close()
	close(release)
	require.NoError(t, <-done)
	after, err := h.parser.Parse(string(uri)+":format", source)
	require.NoError(t, err)
	defer after.Close()
	require.NotEqual(t, before.RootNode().Id(), after.RootNode().Id())
}

func TestUnopenedNavigationUnitsDoNotRetainTrees(t *testing.T) {
	h, _ := runtimeHandler(t)
	uri := fileURI(filepath.Join(h.workspaceRoot, "pipeline.yaml"))
	source := "mapping: |\n  map convert { root = this }\n  root = this.apply(\"convert\")\n"
	units := h.navigationUnits(uri, source, true)
	defer closeRenameUnits(units)
	require.Len(t, units, 1)
	after, err := h.parser.Parse(string(units[0].uri)+":rename", units[0].text)
	require.NoError(t, err)
	defer after.Close()
	require.NotEqual(t, units[0].tree.RootNode().Id(), after.RootNode().Id())
	require.Equal(t, "convert", units[0].symbols[0].name, "returned clones remain readable after cleanup")
	defs, err := h.definitionForText(fileURI(filepath.Join(h.workspaceRoot, "mapping.blobl")), "map convert { root = this }", protocol.Position{Character: strings.Index("map convert", "convert")})
	require.NoError(t, err)
	require.Len(t, defs, 1)
}
