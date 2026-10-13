package lsp

import (
	"context"
	"io"
	"log/slog"

	"github.com/owenrumney/go-lsp/lsp"
	"github.com/owenrumney/go-lsp/server"
)

func NewServer(handler *Handler, logger *slog.Logger) *server.Server {
	return server.NewServer(handler,
		server.WithLogger(logger),
		server.WithCapabilityOptions(server.CapabilityOptions{
			Completion: &lsp.CompletionOptions{TriggerCharacters: []string{".", "@", "$"}},
			ExecuteCommand: &lsp.ExecuteCommandOptions{
				Commands: []string{"bloblang-lsp.showResult"},
			},
		}),
	)
}

func Run(ctx context.Context, handler *Handler, logger *slog.Logger, rw io.ReadWriteCloser) error {
	defer handler.Shutdown(context.Background())
	return NewServer(handler, logger).Run(ctx, rw)
}
