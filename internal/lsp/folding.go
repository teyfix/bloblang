package lsp

import (
	"context"
	"sort"
	"strings"

	protocol "github.com/owenrumney/go-lsp/lsp"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// FoldingRange returns structural folds and consecutive comments/imports. All
// positions go through the same UTF-16 and YAML source mapping as navigation.
func (h *Handler) FoldingRange(ctx context.Context, p *protocol.FoldingRangeParams) ([]protocol.FoldingRange, error) {
	defer h.discardClosedDocumentTrees(hostDocumentURI(p.TextDocument.URI))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text, ok := h.documents.Text(p.TextDocument.URI)
	if !ok {
		return []protocol.FoldingRange{}, nil
	}
	units := h.renameUnits(p.TextDocument.URI, text)
	defer closeRenameUnits(units)
	out := []protocol.FoldingRange{}
	seen := map[[2]int]bool{}
	add := func(r protocol.Range, kind protocol.FoldingRangeKind) {
		key := [2]int{r.Start.Line, r.End.Line}
		if r.Start.Line >= r.End.Line || seen[key] {
			return
		}
		seen[key] = true
		fold := protocol.FoldingRange{StartLine: r.Start.Line, EndLine: r.End.Line, StartCharacter: new(r.Start.Character), EndCharacter: new(r.End.Character)}
		if kind != "" {
			fold.Kind = new(kind)
		}
		out = append(out, fold)
	}
	for _, unit := range units {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if unit.tree == nil {
			continue
		}
		if unit.region != nil && !unit.region.interpolation {
			end := unit.region.end
			for end > unit.region.start && (text[end-1] == '\n' || text[end-1] == '\r') {
				end--
			}
			add(protocol.Range{Start: bytePosition(text, unit.region.start), End: bytePosition(text, end)}, "")
		}
		walkNodes(unit.tree.RootNode(), func(n *tree_sitter.Node) {
			switch n.Kind() {
			case "map_declaration", "statement_block", "object_literal", "array_literal", "call_expr", "method_call", "match_expr", "if_expr", "parenthesized_expr", "string":
				r := unit.referenceLocation(n).Range
				// Leave a closing delimiter on its own line visible for line-only clients.
				localEnd := int(n.EndByte())
				if localEnd > int(n.StartByte()) && strings.ContainsRune("})]", rune(unit.text[localEnd-1])) {
					lastLine := strings.LastIndex(unit.text[:localEnd-1], "\n") + 1
					if lastLine > 0 && strings.TrimSpace(unit.text[lastLine:localEnd-1]) == "" {
						local := protocol.Range{Start: bytePosition(unit.text, int(n.StartByte())), End: bytePosition(unit.text, lastLine-1)}
						if unit.region != nil {
							local = unit.region.hostRange(unit.host, local)
						}
						r = local
					}
				}
				add(r, "")
			}
			var first, last *tree_sitter.Node
			flush := func() {
				if first == nil || last == nil {
					return
				}
				r := unit.referenceLocation(first).Range
				r.End = unit.referenceLocation(last).Range.End
				kind := protocol.FoldingRangeKindComment
				if first.Kind() == "import_statement" {
					kind = protocol.FoldingRangeKindImports
				}
				add(r, kind)
				first, last = nil, nil
			}
			for i := uint(0); i < n.NamedChildCount(); i++ {
				child := n.NamedChild(i)
				if child.Kind() != "comment" && child.Kind() != "import_statement" {
					flush()
					continue
				}
				if last != nil && (last.Kind() != child.Kind() || child.StartPosition().Row > last.EndPosition().Row+1) {
					flush()
				}
				if first == nil {
					first = child
				}
				last = child
			}
			flush()
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartLine != out[j].StartLine {
			return out[i].StartLine < out[j].StartLine
		}
		return out[i].EndLine > out[j].EndLine
	})
	return out, nil
}
