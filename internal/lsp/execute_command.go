package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/teyfix/bloblang/internal/schema"
)

func executeCommandNames() []string {
	return []string{"bloblang-lsp.showResult", "bloblang-lsp.showSchema", "bloblang-lsp.openFile"}
}

func (h *Handler) ExecuteCommand(ctx context.Context, params *protocol.ExecuteCommandParams) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if params == nil {
		return nil, fmt.Errorf("command parameters are required")
	}
	switch params.Command {
	case "bloblang-lsp.openFile":
		if len(params.Arguments) != 1 {
			return nil, fmt.Errorf("%s requires one file URI", params.Command)
		}
		var uriStr string
		if err := json.Unmarshal(params.Arguments[0], &uriStr); err != nil {
			return nil, fmt.Errorf("decode file URI: %w", err)
		}
		if h.client == nil {
			return nil, fmt.Errorf("language client is unavailable")
		}
		return h.client.ShowDocument(ctx, &protocol.ShowDocumentParams{
			URI:       protocol.URI(uriStr),
			TakeFocus: new(true),
		})
	case "bloblang-lsp.showResult", "bloblang-lsp.showSchema":
		if len(params.Arguments) < 1 || len(params.Arguments) > 2 {
			return nil, fmt.Errorf("%s requires a preview payload and optional origin URI", params.Command)
		}
		var origin protocol.DocumentURI
		if len(params.Arguments) > 1 {
			if err := json.Unmarshal(params.Arguments[1], &origin); err != nil {
				return nil, fmt.Errorf("decode preview origin URI: %w", err)
			}
		}
		raw := params.Arguments[0]
		if params.Command == "bloblang-lsp.showSchema" {
			if len(raw) > schema.MaxBytes {
				return nil, fmt.Errorf("schema sample exceeds maximum size of %d bytes", schema.MaxBytes)
			}
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, fmt.Errorf("decode schema sample: %w", err)
			}
			inferred, err := schema.Infer(value, schema.Options{})
			if err != nil {
				return nil, err
			}
			raw, err = json.Marshal(inferred)
			if err != nil {
				return nil, fmt.Errorf("encode schema preview: %w", err)
			}
		}
		return h.showPreview(ctx, origin, string(raw))
	default:
		return nil, fmt.Errorf("unknown command: %s", params.Command)
	}
}

func (h *Handler) showPreview(ctx context.Context, origin protocol.DocumentURI, raw string) (*protocol.ShowDocumentResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if h.client == nil {
		return nil, fmt.Errorf("language client is unavailable")
	}
	lang, body := h.preview(origin, raw)
	tmpFile, err := os.CreateTemp("", "bloblang-sample-*."+lang)
	if err != nil {
		return nil, fmt.Errorf("create preview: %w", err)
	}
	keep := false
	defer func() {
		_ = tmpFile.Close()
		if !keep {
			_ = os.Remove(tmpFile.Name())
		}
	}()
	if _, err := tmpFile.WriteString(body + "\n"); err != nil {
		return nil, fmt.Errorf("write preview: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return nil, fmt.Errorf("close preview: %w", err)
	}
	result, err := h.client.ShowDocument(ctx, &protocol.ShowDocumentParams{
		URI:       protocol.URI(fileURI(tmpFile.Name())),
		TakeFocus: new(true),
	})
	if err != nil {
		return nil, err
	}
	if result == nil || !result.Success {
		return nil, fmt.Errorf("client did not open the preview document")
	}
	// Keep successful previews available for the editor to read and revisit.
	keep = true
	return result, nil
}
