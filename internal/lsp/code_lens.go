package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dustin/go-humanize"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

func (h *Handler) CodeLens(ctx context.Context, params *protocol.CodeLensParams) ([]protocol.CodeLens, error) {
	uri := params.TextDocument.URI
	ctx, version, text, sample, done, ok := h.requestDocument(ctx, uri)
	defer done()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ok {
		return []protocol.CodeLens{}, nil
	}
	if yamlDocument(uri) && !strings.Contains(string(uri), "#bloblang-") {
		host := text
		out := []protocol.CodeLens{}
		for _, r := range extractRegions(uri, host) {
			cp := *params
			cp.TextDocument.URI = r.uri
			vals, err := h.CodeLens(ctx, &cp)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				continue
			}
			for _, v := range vals {
				v.Range = r.hostRange(host, v.Range)
				out = append(out, v)
			}
		}
		return out, nil
	}
	if sample == nil {
		return []protocol.CodeLens{}, nil
	}

	lenses := make([]protocol.CodeLens, 0)
	var execErrs []protocol.Diagnostic
	severity := protocol.SeverityWarning

	tree, parseErr := h.parser.Parse(string(uri), text)
	if parseErr != nil || tree == nil {
		return []protocol.CodeLens{}, nil
	}

	defer tree.Close()
	root := tree.RootNode()

	for _, path := range sample.Dependencies {
		row := sample.DependencyLines[path]
		lenses = append(lenses, protocol.CodeLens{Range: protocol.Range{Start: protocol.Position{Line: row}, End: protocol.Position{Line: row}}, Command: &protocol.Command{Title: "[Open Sample]", Command: "bloblang-lsp.openFile", Arguments: []json.RawMessage{rawJSON(string(fileURI(path)))}}})
	}

	// 2. Traverse statements to generate statement level Show Input / Show Output code lenses
	for i := uint(0); i < root.ChildCount(); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		child := root.Child(i)
		if child.Kind() != "root_assignment" && child.Kind() != "if_statement" {
			continue
		}

		startRow := int(child.StartPosition().Row)

		// Calculate cumulative state BEFORE this assignment
		resultBefore, errBefore := h.executor.ExecuteThroughContext(ctx, h.parser, string(uri), sample, text, child.StartByte())
		if errBefore != nil {
			continue
		}

		if resultBefore != nil && !resultBefore.Deleted && resultBefore.Truncated {
			lenses = append(lenses, protocol.CodeLens{
				Range: protocol.Range{
					Start: protocol.Position{Line: startRow, Character: 0},
					End:   protocol.Position{Line: startRow, Character: 0},
				},
				Command: &protocol.Command{
					Title:     fmt.Sprintf("Show Input (%s)", humanize.Bytes(uint64(len(resultBefore.Full)))),
					Command:   "bloblang-lsp.showResult",
					Arguments: []json.RawMessage{[]byte(resultBefore.Full), rawJSON(string(uri))},
				},
			})
			lenses = append(lenses, schemaCodeLens(startRow, "Input", resultBefore.Full, uri))
		}

		// Calculate cumulative state AFTER this assignment
		resultAfter, errAfter := h.executor.ExecuteThroughContext(ctx, h.parser, string(uri), sample, text, child.EndByte())
		if errAfter != nil {
			execErrs = append(execErrs, protocol.Diagnostic{
				Range: protocol.Range{
					Start: protocol.Position{Line: startRow, Character: int(child.StartPosition().Column)},
					End:   protocol.Position{Line: int(child.EndPosition().Row), Character: int(child.EndPosition().Column)},
				},
				Severity: &severity,
				Source:   "bloblang",
				Message:  indentMessage(errAfter.Error()),
			})
			continue
		}

		if resultAfter != nil && !resultAfter.Deleted && resultAfter.Truncated {
			lenses = append(lenses, protocol.CodeLens{
				Range: protocol.Range{
					Start: protocol.Position{Line: startRow, Character: 0},
					End:   protocol.Position{Line: startRow, Character: 0},
				},
				Command: &protocol.Command{
					Title:     fmt.Sprintf("Show Output (%s)", humanize.Bytes(uint64(len(resultAfter.Full)))),
					Command:   "bloblang-lsp.showResult",
					Arguments: []json.RawMessage{[]byte(resultAfter.Full), rawJSON(string(uri))},
				},
			})
			lenses = append(lenses, schemaCodeLens(startRow, "Output", resultAfter.Full, uri))
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for i := range execErrs {
		execErrs[i].Range = rangeUTF16(text, execErrs[i].Range)
	}
	h.publishExecDiagnosticsVersion(ctx, uri, execErrs, version)
	return lenses, nil
}

// Carry the existing preview payload without inferring on each lens request.
// The schema is generated only when the client executes the command.
func schemaCodeLens(row int, direction, raw string, uri protocol.DocumentURI) protocol.CodeLens {
	return protocol.CodeLens{
		Range: protocol.Range{Start: protocol.Position{Line: row}, End: protocol.Position{Line: row}},
		Command: &protocol.Command{
			Title:     "Show " + direction + " Schema",
			Command:   "bloblang-lsp.showSchema",
			Arguments: []json.RawMessage{[]byte(raw), rawJSON(string(uri))},
		},
	}
}
