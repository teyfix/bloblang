package lsp

import (
	"context"
	"strings"

	protocol "github.com/owenrumney/go-lsp/lsp"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// DocumentSymbol exposes map declarations and assignments without evaluating the
// mapping. Nested assignments stay under their containing map in the outline.
func (h *Handler) DocumentSymbol(ctx context.Context, p *protocol.DocumentSymbolParams) ([]protocol.DocumentSymbol, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text, ok := h.documents.Text(p.TextDocument.URI)
	if !ok {
		return []protocol.DocumentSymbol{}, nil
	}
	units := h.renameUnits(p.TextDocument.URI, text)
	defer closeRenameUnits(units)
	out := []protocol.DocumentSymbol{}
	for _, unit := range units {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if unit.tree == nil || unit.region != nil && unit.region.interpolation {
			continue
		}
		out = append(out, outlineSymbols(unit, unit.tree.RootNode())...)
	}
	return out, nil
}

func outlineSymbols(unit renameUnit, n *tree_sitter.Node) []protocol.DocumentSymbol {
	var out []protocol.DocumentSymbol
	for i := uint(0); i < n.NamedChildCount(); i++ {
		child := n.NamedChild(i)
		symbol, ok := outlineSymbol(unit, child)
		if ok {
			if child.Kind() == "map_declaration" {
				symbol.Children = outlineSymbols(unit, child)
			}
			out = append(out, symbol)
		} else if child.Kind() == "if_statement" || child.Kind() == "statement_block" {
			out = append(out, outlineSymbols(unit, child)...)
		}
	}
	return out
}

func outlineSymbol(unit renameUnit, n *tree_sitter.Node) (protocol.DocumentSymbol, bool) {
	var selection *tree_sitter.Node
	var name, detail string
	kind := protocol.SymbolKindVariable
	switch n.Kind() {
	case "map_declaration":
		selection = n.ChildByFieldName("name")
		kind, detail = protocol.SymbolKindFunction, "map"
	case "let_assignment":
		selection = n.ChildByFieldName("name")
		detail = "let"
	case "root_assignment", "meta_assignment":
		kind = protocol.SymbolKindProperty
		start := int(n.StartByte())
		end := start
		// The assignment's left side is bounded by its direct '=' token, including
		// quoted path segments that may themselves contain equals signs.
		for i := uint(0); i < n.ChildCount(); i++ {
			if child := n.Child(i); child.Kind() == "=" {
				end = int(child.StartByte())
				break
			}
		}
		name = strings.TrimSpace(unit.text[start:end])
		if name == "" {
			return protocol.DocumentSymbol{}, false
		}
		r := protocol.Range{Start: bytePosition(unit.text, start), End: bytePosition(unit.text, start+len(name))}
		if unit.region != nil {
			r = unit.region.hostRange(unit.host, r)
		}
		return protocol.DocumentSymbol{Name: name, Kind: kind, Range: unit.referenceLocation(n).Range, SelectionRange: r}, true
	default:
		return protocol.DocumentSymbol{}, false
	}
	if selection == nil {
		return protocol.DocumentSymbol{}, false
	}
	name = strings.Trim(selection.Utf8Text([]byte(unit.text)), "\"")
	if strings.TrimSpace(name) == "" {
		return protocol.DocumentSymbol{}, false
	}
	return protocol.DocumentSymbol{Name: name, Detail: detail, Kind: kind, Range: unit.referenceLocation(n).Range, SelectionRange: unit.referenceLocation(selection).Range}, true
}
