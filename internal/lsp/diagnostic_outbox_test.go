package lsp

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/stretchr/testify/require"
)

// The reader keeps Run alive while the first outbound write is blocked. Later
// payloads are captured without requiring an independently running LSP client.
type blockedDiagnosticTransport struct {
	reading, writing, release, closed chan struct{}
	payloads                          chan []byte
	readOnce, writeOnce, closeOnce    sync.Once
}

func (rw *blockedDiagnosticTransport) Read([]byte) (int, error) {
	rw.readOnce.Do(func() { close(rw.reading) })
	<-rw.closed
	return 0, io.EOF
}
func (rw *blockedDiagnosticTransport) Write(p []byte) (int, error) {
	rw.writeOnce.Do(func() { close(rw.writing) })
	select {
	case <-rw.release:
	case <-rw.closed:
		return 0, io.ErrClosedPipe
	}
	if len(p) > 0 && p[0] == '{' {
		rw.payloads <- append([]byte(nil), p...)
	}
	return len(p), nil
}
func (rw *blockedDiagnosticTransport) Close() error {
	rw.closeOnce.Do(func() { close(rw.closed) })
	return nil
}

func TestBlockedDiagnosticTransportDoesNotBlockLifecycle(t *testing.T) {
	h, uri := runtimeHandler(t)
	rw := &blockedDiagnosticTransport{reading: make(chan struct{}), writing: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}), payloads: make(chan []byte, 16)}
	defer rw.Close()
	serverDone := make(chan struct{})
	go func() { _ = Run(context.Background(), h, h.logger, rw); close(serverDone) }()
	select {
	case <-rw.reading:
	case <-time.After(time.Second):
		t.Fatal("server did not start")
	}
	openFeature(t, h, uri, "root = this")
	publish := func(uri protocol.DocumentURI, message string) {
		h.mu.Lock()
		h.publishDiagnostics(context.Background(), uri, []protocol.Diagnostic{{Message: message}}, h.latestVersion[uri])
		h.mu.Unlock()
	}
	publish(uri, "already in flight")
	select {
	case <-rw.writing:
	case <-time.After(time.Second):
		t.Fatal("first write did not start")
	}

	other := protocol.DocumentURI("file:///queued.blobl")
	openFeature(t, h, other, "root = this")
	publish(other, "obsolete queued diagnostic")
	require.NoError(t, h.DidChange(context.Background(), &protocol.DidChangeTextDocumentParams{TextDocument: protocol.VersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: other}, Version: 2}, ContentChanges: []protocol.TextDocumentContentChangeEvent{{Text: "root = 1"}}}))
	closed := make(chan error, 1)
	go func() {
		closed <- h.DidClose(context.Background(), &protocol.DidCloseTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}})
	}()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("document close blocked behind transport I/O")
	}
	openFeature(t, h, uri, "root = 2")
	for range 10 {
		publish(uri, "current revision")
	}
	barrier := protocol.DocumentURI("file:///barrier.blobl")
	h.publishDiagnostics(context.Background(), barrier, nil)
	close(rw.release)
	var publications []protocol.PublishDiagnosticsParams
	for range 3 {
		select {
		case payload := <-rw.payloads:
			var message struct {
				Params protocol.PublishDiagnosticsParams `json:"params"`
			}
			require.NoError(t, json.Unmarshal(payload, &message))
			publications = append(publications, message.Params)
		case <-time.After(time.Second):
			t.Fatal("queued diagnostics did not drain")
		}
	}
	require.Equal(t, "already in flight", publications[0].Diagnostics[0].Message)
	require.Equal(t, uri, publications[1].URI)
	require.Equal(t, "current revision", publications[1].Diagnostics[0].Message)
	require.Equal(t, barrier, publications[2].URI, "old generations are skipped and repeated revisions are coalesced")
	require.NoError(t, rw.Close())
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}
