package lsp

import (
	"context"
	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/redpanda-data/benthos/v4/public/bloblang"
	"github.com/teyfix/bloblang/internal/benthos"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"sort"
	"strings"
)

func (h *Handler) methodCompletion(ctx context.Context, uri protocol.DocumentURI, sample *benthos.Sample, text string, pos protocol.Position, items []protocol.CompletionItem) []protocol.CompletionItem {
	if sample == nil {
		return items
	}
	offset := positionByte(text, pos)
	start := offset
	for start > 0 && isIdentChar(text[start-1]) {
		start--
	}
	if start == 0 || text[start-1] != '.' {
		return items
	}
	candidate := text[:start] + "__bloblang_lsp_completion()"
	tree, err := h.parser.Parse(string(uri)+":completion", candidate)
	if err != nil {
		return items
	}
	defer tree.Close()
	var object, statement *tree_sitter.Node
	walkNodes(tree.RootNode(), func(n *tree_sitter.Node) {
		if n.Kind() == "method_call" {
			method := n.ChildByFieldName("method")
			if method != nil && method.Utf8Text([]byte(candidate)) == "__bloblang_lsp_completion" {
				object = n.ChildByFieldName("object")
				statement = n
				for statement != nil && !benthosStatement(statement.Kind()) {
					statement = statement.Parent()
				}
			}
		}
	})
	if object == nil || statement == nil || hoverExpression(object) == nil {
		return items
	}
	result, err := h.executor.EvaluateExpressionContext(ctx, h.parser, string(uri)+":completion", sample, candidate, statement, object)
	if err != nil || result == nil || result.Deleted || result.Value == nil {
		return items
	}
	kind := valueType(result.Value)
	allowed := map[string]bool{}
	h.benv.WalkMethods(func(name string, view *bloblang.MethodView) {
		data := view.TemplateData()
		if len(data.Categories) == 0 {
			allowed[name] = true
			return
		}
		for _, c := range data.Categories {
			switch c.Category {
			case "String Manipulation", "Regular Expressions":
				if kind == "string" || kind == "bytes" {
					allowed[name] = true
				}
			case "Number Manipulation":
				if kind == "number" {
					allowed[name] = true
				}
			case "Object & Array Manipulation":
				if kind == "object" || kind == "array" {
					allowed[name] = true
				}
			default:
				allowed[name] = true
			}
		}
	})
	var out []protocol.CompletionItem
	for _, item := range items {
		name := item.FilterText
		if name == "" {
			name = item.Label
			for strings.HasPrefix(name, "[") {
				if i := strings.Index(name, "] "); i >= 0 {
					name = name[i+2:]
				} else {
					break
				}
			}
			if i := strings.IndexAny(name, "( "); i >= 0 {
				name = name[:i]
			}
		}
		if allowed[name] {
			out = append(out, item)
		}
	}
	if obj, ok := result.Value.(map[string]any); ok {
		keys := make([]string, 0, len(obj))
		for key := range obj {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		fieldKind := protocol.CompletionItemKindField
		for _, key := range keys {
			if !validIdentifier(key) {
				continue
			}
			out = append(out, protocol.CompletionItem{Label: key, Kind: &fieldKind, InsertText: key, Detail: "Sample field (" + valueType(obj[key]) + ")", SortText: "0_" + key})
		}
	}
	return out
}
func valueType(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case []byte:
		return "bytes"
	case bool:
		return "bool"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case nil:
		return "null"
	default:
		return "number"
	}
}
func validIdentifier(s string) bool {
	if s == "" || s[0] >= '0' && s[0] <= '9' {
		return false
	}
	for i := range []byte(s) {
		if !isIdentChar(s[i]) {
			return false
		}
	}
	return true
}
func (h *Handler) variableCompletions(uri protocol.DocumentURI, text string, pos protocol.Position) []protocol.CompletionItem {
	offset := positionByte(text, pos)
	start := offset
	for start > 0 && isIdentChar(text[start-1]) {
		start--
	}
	if start == 0 {
		return nil
	}
	prefix := text[start-1]
	kind := protocol.CompletionItemKindVariable
	var items []protocol.CompletionItem
	if prefix == '@' {
		if sample := h.getSample(uri); sample != nil {
			for key := range sample.Meta {
				items = append(items, protocol.CompletionItem{Label: key, Kind: &kind, InsertText: key, Detail: "Sample metadata"})
			}
		}
		return items
	}
	tree, err := h.parser.Parse(string(uri), text)
	if err != nil {
		return items
	}
	defer tree.Close()
	scope := tree.RootNode().DescendantForByteRange(uint(offset), uint(offset))
	if scope == nil {
		return items
	}
	ss := symbols(tree.RootNode(), text)
	seen := map[string]bool{}
	for _, s := range ss {
		if s.kind != "variable" || !s.declaration || s.node.StartByte() > uint(offset) || seen[s.name] {
			continue
		}
		probe := symbol{name: s.name, kind: "variable", node: scope, scope: scopeOf(scope)}
		if visibleDefinition(ss, &probe) != nil {
			seen[s.name] = true
			items = append(items, protocol.CompletionItem{Label: s.name, Kind: &kind, InsertText: s.name, Detail: "Local variable"})
		}
	}
	return items
}
