package lsp

import (
	"bytes"
	"encoding/json"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/teyfix/bloblang/internal/benthos"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

func benthosStatement(kind string) bool {
	switch kind {
	case "root_assignment", "let_assignment", "meta_assignment", "map_declaration", "if_statement":
		return true
	}
	return false
}
func hoverExpression(n *tree_sitter.Node) *tree_sitter.Node {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Kind() == "lambda" || p.Kind() == "map_declaration" {
			return nil
		}
		if p.Kind() == "method_call" {
			method := p.ChildByFieldName("method")
			object := p.ChildByFieldName("object")
			if method != nil && object != nil && n.StartByte() > object.EndByte() {
				return nil
			}
		}
	}

	// Identifier names of methods/functions keep documentation; hovering their receiver
	// or closing argument evaluates the corresponding complete expression.
	if n.Kind() == "identifier" && n.Parent() != nil {
		p := n.Parent()
		if (p.Kind() == "method_call" && p.ChildByFieldName("method") != nil && p.ChildByFieldName("method").Id() == n.Id()) || p.Kind() == "call_expr" {
			return nil
		}
	}
	switch n.Kind() {
	case "this_ref", "variable_ref", "meta_ref", "field_access", "method_call", "call_expr", "binary_expr", "string", "number", "boolean", "null":
	default:
		if p := n.Parent(); p != nil {
			switch p.Kind() {
			case "field_access", "method_call", "call_expr", "variable_ref", "meta_ref", "binary_expr":
				n = p
			default:
				return nil
			}
		} else {
			return nil
		}
	}
	for n.Parent() != nil && n.Parent().Kind() == "field_access" {
		n = n.Parent()
	}
	return n
}
func (h *Handler) valueHover(uri protocol.DocumentURI, r *benthos.PartialResult, n *tree_sitter.Node) *protocol.Hover {
	body := h.previewMarkdown(uri, r.Full)
	if r.Deleted {
		body = "`deleted()` — deletion marker (removes a field or filters a message, depending on assignment)"
	}
	return &protocol.Hover{Contents: protocol.NewHoverContents(protocol.Markdown, body), Range: &protocol.Range{Start: protocol.Position{Line: int(n.StartPosition().Row), Character: int(n.StartPosition().Column)}, End: protocol.Position{Line: int(n.EndPosition().Row), Character: int(n.EndPosition().Column)}}}
}

func prettyResult(s string) string {
	var out bytes.Buffer
	if json.Indent(&out, []byte(s), "", "  ") == nil {
		return out.String()
	}
	return s
}
