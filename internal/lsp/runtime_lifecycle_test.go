package lsp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/owenrumney/go-lsp/servertest"
	"github.com/redpanda-data/benthos/v4/public/bloblang"
	"github.com/stretchr/testify/require"
	"github.com/teyfix/bloblang-lsp/internal/benthos"
)

func runtimeHandler(t *testing.T) (*Handler, protocol.DocumentURI) {
	t.Helper()
	h, uri := featureHandler(t)
	// Tests drive validation explicitly, avoiding unrelated timer executions.
	h.config.DiagnosticsDebounce = time.Hour
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
	return h, uri
}

func TestInlayRangeCancellationAndPrefixReuse(t *testing.T) {
	h, uri := runtimeHandler(t)
	var calls atomic.Int64
	require.NoError(t, h.benv.RegisterFunctionV2("runtime_tick", bloblang.NewPluginSpec().Impure(), func(*bloblang.ParsedParams) (bloblang.Function, error) {
		return func() (any, error) { return calls.Add(1), nil }, nil
	}))
	var lines []string
	for i := range 10 {
		lines = append(lines, fmt.Sprintf("root.x%d = runtime_tick()", i))
	}
	text := strings.Join(lines, "\n")
	_, err := h.documents.Open(&protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{URI: uri, Text: text}})
	require.NoError(t, err)
	h.samples[uri] = &benthos.Sample{Value: map[string]any{}}
	p := &protocol.InlayHintParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Range: protocol.Range{End: protocol.Position{Line: 0, Character: len(lines[0])}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = h.InlayHint(ctx, p)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, calls.Load())
	hints, err := h.InlayHint(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, hints, 2)
	require.Equal(t, int64(1), calls.Load())
	for _, hint := range hints {
		require.Zero(t, hint.Position.Line)
	}
	p.Range.End = bytePosition(text, len(text))
	hints, err = h.InlayHint(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, hints, 20)
	require.Equal(t, int64(55), calls.Load(), "each distinct prefix executes once; adjacent before/after previews share it")
	_, err = h.CodeLens(context.Background(), &protocol.CodeLensParams{TextDocument: p.TextDocument})
	require.NoError(t, err)
	require.Equal(t, int64(55), calls.Load(), "code lenses reuse the same revision's results")
	_, err = h.InlayHint(context.Background(), p)
	require.NoError(t, err)
	require.Equal(t, int64(55), calls.Load())
}

func TestInlayRangesUseUTF16PositionsAndYAMLRegions(t *testing.T) {
	h, uri := runtimeHandler(t)
	text := "#!input {}\nroot.x = [\n  \"😀\"\n]\nroot.y = \"😀\""
	openFeature(t, h, uri, text)
	for _, span := range []protocol.Range{
		{Start: protocol.Position{Line: 1}, End: protocol.Position{Line: 1, Character: 10}},
		{Start: protocol.Position{Line: 3}, End: protocol.Position{Line: 3, Character: 1}},
		{Start: protocol.Position{Line: 4, Character: 13}, End: protocol.Position{Line: 4, Character: 13}},
	} {
		hints, err := h.InlayHint(context.Background(), &protocol.InlayHintParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Range: span})
		require.NoError(t, err)
		require.Len(t, hints, 1, "%+v", span)
		require.True(t, positionInRange(hints[0].Position, span))
	}
	yamlURI := fileURI(filepath.Join(h.workspaceRoot, "pipeline.yaml"))
	yamlText := "first:\n  mapping: |\n    #!input {}\n    root.x = 1\nsecond:\n  mapping: |\n    #!input {}\n    root.y = 2\n"
	openFeature(t, h, yamlURI, yamlText)
	span := protocol.Range{Start: protocol.Position{Line: 7}, End: protocol.Position{Line: 7, Character: 20}}
	hints, err := h.InlayHint(context.Background(), &protocol.InlayHintParams{TextDocument: protocol.TextDocumentIdentifier{URI: yamlURI}, Range: span})
	require.NoError(t, err)
	require.Len(t, hints, 2)
	for _, hint := range hints {
		require.Equal(t, 7, hint.Position.Line)
	}
}

func TestValidationCannotPublishAfterCloseAndReopen(t *testing.T) {
	h, uri := runtimeHandler(t)
	entered, release := make(chan struct{}), make(chan struct{})
	require.NoError(t, h.benv.RegisterFunctionV2("runtime_gate", bloblang.NewPluginSpec().Impure(), func(*bloblang.ParsedParams) (bloblang.Function, error) {
		close(entered)
		<-release
		return nil, errors.New("old document validation error")
	}))
	openFeature(t, h, uri, "root = runtime_gate()")
	h.mu.Lock()
	oldContext, oldVersion := h.documentContexts[uri], h.latestVersion[uri]
	h.mu.Unlock()
	done := make(chan struct{})
	go func() { h.validateDocument(oldContext, uri, oldVersion); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("validation did not start")
	}
	require.NoError(t, h.DidClose(context.Background(), &protocol.DidCloseTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}))
	openFeature(t, h, uri, "#!input {}\nroot = this")
	h.mu.Lock()
	newContext, newVersion := h.documentContexts[uri], h.latestVersion[uri]
	h.mu.Unlock()
	require.Greater(t, newVersion, oldVersion)
	h.validateDocument(newContext, uri, newVersion)
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old validation did not finish")
	}
	h.lastDiagnosticsMu.RLock()
	diagnostics := append([]protocol.Diagnostic(nil), h.lastDiagnostics[uri]...)
	h.lastDiagnosticsMu.RUnlock()
	for _, diagnostic := range diagnostics {
		require.NotContains(t, diagnostic.Message, "old document")
	}
	require.Empty(t, diagnostics)
}

func TestCloseClearsExecutionDiagnosticsOnWire(t *testing.T) {
	h, uri := runtimeHandler(t)
	client := servertest.New(t, h)
	openFeature(t, h, uri, "root = this")
	h.publishExecDiagnostics(context.Background(), uri, []protocol.Diagnostic{{Message: "execution failure"}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := client.WaitForDiagnostics(ctx, uri)
	require.NoError(t, err)
	client.ClearDiagnostics()
	require.NoError(t, h.DidClose(ctx, &protocol.DidCloseTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}))
	diagnostics, err := client.WaitForDiagnostics(ctx, uri)
	require.NoError(t, err)
	require.NotNil(t, diagnostics)
	require.Empty(t, diagnostics)
	h.execDiagnosticsMu.RLock()
	require.NotContains(t, h.execDiagnostics, uri)
	h.execDiagnosticsMu.RUnlock()
}

func TestCanceledInlayCannotRestoreClosedDocumentErrors(t *testing.T) {
	h, uri := runtimeHandler(t)
	entered, release := make(chan struct{}), make(chan struct{})
	require.NoError(t, h.benv.RegisterFunctionV2("runtime_gate", bloblang.NewPluginSpec().Impure(), func(*bloblang.ParsedParams) (bloblang.Function, error) {
		return func() (any, error) { close(entered); <-release; return nil, errors.New("old execution error") }, nil
	}))
	openFeature(t, h, uri, "#!input {}\nroot = runtime_gate()")
	done := make(chan error, 1)
	go func() {
		_, err := h.InlayHint(context.Background(), &protocol.InlayHintParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Range: protocol.Range{End: protocol.Position{Line: 10}}})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("preview did not start")
	}
	require.NoError(t, h.DidClose(context.Background(), &protocol.DidCloseTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}))
	openFeature(t, h, uri, "#!input {}\nroot = this")
	close(release)
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("preview did not finish")
	}
	h.execDiagnosticsMu.RLock()
	require.Empty(t, h.execDiagnostics[uri])
	h.execDiagnosticsMu.RUnlock()
}

func TestNarrowInlayRangePreservesUnevaluatedMultilineErrors(t *testing.T) {
	h, uri := runtimeHandler(t)
	text := "#!input {}\nroot = [\n  throw(\"bad value\")\n]"
	openFeature(t, h, uri, text)
	request := &protocol.InlayHintParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Range: protocol.Range{End: bytePosition(text, len(text))}}
	_, err := h.InlayHint(context.Background(), request)
	require.NoError(t, err)
	h.execDiagnosticsMu.RLock()
	require.Len(t, h.execDiagnostics[uri], 1)
	h.execDiagnosticsMu.RUnlock()
	request.Range = protocol.Range{Start: protocol.Position{Line: 1}, End: protocol.Position{Line: 1, Character: 8}}
	_, err = h.InlayHint(context.Background(), request)
	require.NoError(t, err)
	h.execDiagnosticsMu.RLock()
	require.Len(t, h.execDiagnostics[uri], 1)
	h.execDiagnosticsMu.RUnlock()
}

func TestUntitledPreviewSharesImportBaseAndDependencyInvalidation(t *testing.T) {
	h, _ := runtimeHandler(t)
	path := filepath.Join(h.workspaceRoot, "shared.blobl")
	require.NoError(t, os.WriteFile(path, []byte(`map named { root = "before" }`), 0600))
	uri := protocol.DocumentURI("untitled:///mapping")
	text := "#!input {}\nimport \"shared.blobl\"\nroot = this.apply(\"named\")"
	openFeature(t, h, uri, text)
	preview := func() string {
		sample := h.getSample(uri)
		r, err := h.executor.ExecuteThrough(h.parser, string(uri), sample, text, uint(len(text)))
		require.NoError(t, err)
		return r.Full
	}
	require.Equal(t, `"before"`, preview())
	require.NoError(t, os.WriteFile(path, []byte(`map named { root = "after" }`), 0600))
	require.NoError(t, h.DidChangeWatchedFiles(context.Background(), &protocol.DidChangeWatchedFilesParams{}))
	require.Equal(t, `"after"`, preview())
}

func TestRunClosesParserAndCancelsDocumentsOnEOF(t *testing.T) {
	h, uri := runtimeHandler(t)
	openFeature(t, h, uri, "root = this")
	h.mu.Lock()
	lifecycle := h.documentContexts[uri]
	h.mu.Unlock()
	server, peer := net.Pipe()
	defer server.Close()
	require.NoError(t, peer.Close())
	_ = Run(context.Background(), h, h.logger, server)
	require.ErrorIs(t, lifecycle.Err(), context.Canceled)
	_, err := h.parser.Parse(string(uri), "root = this")
	require.ErrorContains(t, err, "closed")
}
