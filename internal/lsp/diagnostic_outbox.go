package lsp

import (
	"context"
	"sync"

	protocol "github.com/owenrumney/go-lsp/lsp"
)

type diagnosticPublication struct {
	params          protocol.PublishDiagnosticsParams
	generation      uint64
	checkGeneration bool
}

// Keep only the newest pending publication for each document. A single sender
// orders writes, so a close/new revision follows any already in-flight write.
// No handler or outbox mutex is held during transport I/O.
type diagnosticOutbox struct {
	mu      sync.Mutex
	pending map[protocol.DocumentURI]diagnosticPublication
	order   []protocol.DocumentURI
	wake    chan struct{}
	cancel  context.CancelFunc
	stopped bool
}

func (h *Handler) enqueueDiagnostics(uri protocol.DocumentURI, diagnostics []protocol.Diagnostic, generation ...uint64) {
	outbox := &h.diagnosticOutbox
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if outbox.stopped {
		return
	}
	if outbox.pending == nil {
		outbox.pending = make(map[protocol.DocumentURI]diagnosticPublication)
		outbox.wake = make(chan struct{}, 1)
		ctx, cancel := context.WithCancel(context.Background())
		outbox.cancel = cancel
		go h.sendDiagnostics(ctx, outbox.wake)
	}
	publication := diagnosticPublication{params: protocol.PublishDiagnosticsParams{URI: uri, Diagnostics: diagnostics}}
	if len(generation) > 0 {
		publication.generation, publication.checkGeneration = generation[0], true
	}
	if _, exists := outbox.pending[uri]; !exists {
		outbox.order = append(outbox.order, uri)
	}
	outbox.pending[uri] = publication
	select {
	case outbox.wake <- struct{}{}:
	default:
	}
}

func (h *Handler) sendDiagnostics(ctx context.Context, wake <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
		}
		for {
			publication, ok := h.diagnosticOutbox.next()
			if !ok {
				break
			}
			h.mu.Lock()
			current := !h.stopped && (!publication.checkGeneration || h.latestVersion[hostDocumentURI(publication.params.URI)] == publication.generation)
			h.mu.Unlock()
			if !current || ctx.Err() != nil {
				continue
			}
			if err := h.client.PublishDiagnostics(ctx, &publication.params); err != nil {
				h.logger.Debug("publish diagnostics failed", "uri", publication.params.URI, "err", err)
			}
		}
	}
}

func (outbox *diagnosticOutbox) next() (diagnosticPublication, bool) {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if len(outbox.order) == 0 {
		return diagnosticPublication{}, false
	}
	uri := outbox.order[0]
	outbox.order[0] = ""
	outbox.order = outbox.order[1:]
	publication := outbox.pending[uri]
	delete(outbox.pending, uri)
	return publication, true
}

func (outbox *diagnosticOutbox) stop() {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	outbox.stopped = true
	if outbox.cancel != nil {
		outbox.cancel()
	}
	clear(outbox.pending)
	outbox.order = nil
}
