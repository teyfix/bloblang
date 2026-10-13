package lsp

import (
	"context"
	"strings"
	"time"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/teyfix/bloblang-lsp/internal/benthos"
)

func hostDocumentURI(uri protocol.DocumentURI) protocol.DocumentURI {
	return protocol.DocumentURI(strings.SplitN(string(uri), "#bloblang-", 2)[0])
}

// The caller holds mu through the document/sample mutation. Reopened documents
// receive a new generation even when the client restarts its version counter.
func (h *Handler) beginDocumentVersion(uri protocol.DocumentURI) (context.Context, uint64) {
	if cancel := h.documentCancels[uri]; cancel != nil {
		cancel()
	}
	h.nextVersion++
	h.latestVersion[uri] = h.nextVersion
	ctx, cancel := context.WithCancel(context.Background())
	h.documentContexts[uri], h.documentCancels[uri] = ctx, cancel
	return ctx, h.nextVersion
}

func (h *Handler) scheduleValidation(ctx context.Context, uri protocol.DocumentURI, version uint64) {
	go func() {
		if delay := h.config.DiagnosticsDebounce; delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
		if ctx.Err() != nil {
			return
		}
		h.validateDocument(ctx, uri, version)
		if ctx.Err() == nil {
			h.scheduleRefresh(uri)
		}
	}()
}

// requestDocument captures a coherent source/sample revision and couples request
// cancellation to edits and document close. Tests that open the store directly
// have no lifecycle context but still honor their caller's cancellation.
func (h *Handler) requestDocument(ctx context.Context, uri protocol.DocumentURI) (context.Context, uint64, string, *benthos.Sample, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	host := hostDocumentURI(uri)
	text, ok := h.documents.Text(uri)
	request, cancel := context.WithCancel(ctx)
	var combined context.Context = request
	stop := func() bool { return false }
	if lifecycle := h.documentContexts[host]; lifecycle != nil {
		stop = context.AfterFunc(lifecycle, cancel)
		combined = revisionContext{Context: request, revision: lifecycle}
		if lifecycle.Err() != nil {
			cancel()
		}
	}
	if h.stopped {
		cancel()
		ok = false
	}
	return combined, h.latestVersion[host], text, h.getSample(uri), func() {
		stop()
		cancel()
		h.discardClosedDocumentTrees(host)
	}, ok
}

// AfterFunc delivers Done asynchronously. Err also checks the document context
// directly so superseded work cannot publish/cache during that delivery gap.
type revisionContext struct {
	context.Context
	revision context.Context
}

func (c revisionContext) Err() error {
	if err := c.revision.Err(); err != nil {
		return err
	}
	return c.Context.Err()
}

func (h *Handler) discardClosedDocumentTrees(uri protocol.DocumentURI) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, open := h.documents.Text(uri); !open {
		h.parser.InvalidateDocument(string(uri))
	}
}

func (h *Handler) publishExecDiagnosticsVersion(ctx context.Context, uri protocol.DocumentURI, diagnostics []protocol.Diagnostic, version uint64, scope ...protocol.Range) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ctx.Err() != nil || h.stopped || h.latestVersion[hostDocumentURI(uri)] != version {
		return
	}
	if _, open := h.documents.Text(uri); !open {
		return
	}
	if len(scope) > 0 {
		h.execDiagnosticsMu.RLock()
		for _, previous := range h.execDiagnostics[uri] {
			// Inlays evaluate a statement's result only when its end position is
			// requested. A range containing just its opening line cannot clear it.
			if !positionInRange(previous.Range.End, scope[0]) {
				diagnostics = append(diagnostics, previous)
			}
		}
		h.execDiagnosticsMu.RUnlock()
	}
	h.publishExecDiagnostics(ctx, uri, diagnostics)
}

func positionBefore(a, b protocol.Position) bool {
	return a.Line < b.Line || a.Line == b.Line && a.Character < b.Character
}
func positionInRange(position protocol.Position, span protocol.Range) bool {
	return !positionBefore(position, span.Start) && !positionBefore(span.End, position)
}
func regionRequestRange(region embeddedRegion, host string, requested protocol.Range) (protocol.Range, bool) {
	start, end := positionByte(host, requested.Start), positionByte(host, requested.End)
	first, last := -1, -1
	for i, offset := range region.offsets {
		if start <= offset && offset <= end {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return protocol.Range{}, false
	}
	return protocol.Range{Start: bytePosition(region.text, first), End: bytePosition(region.text, last)}, true
}
