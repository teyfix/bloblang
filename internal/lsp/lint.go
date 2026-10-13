package lsp

import (
	"context"
	"fmt"
	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/teyfix/bloblang-lsp/internal/editorconfig"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"go.yaml.in/yaml/v3"
	"strings"
)

type lintFinding struct {
	diagnostic  protocol.Diagnostic
	replacement string
}

func nodeText(n *tree_sitter.Node, s string) string {
	if n == nil {
		return ""
	}
	return n.Utf8Text([]byte(s))
}
func methodName(n *tree_sitter.Node, s string) string {
	if n == nil || n.Kind() != "method_call" {
		return ""
	}
	return nodeText(n.ChildByFieldName("method"), s)
}
func methodArgs(n *tree_sitter.Node) []*tree_sitter.Node {
	var out []*tree_sitter.Node
	if n == nil {
		return out
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if a := n.ChildByFieldName("object"); a != nil && a.Id() == c.Id() {
			continue
		}
		if a := n.ChildByFieldName("method"); a != nil && a.Id() == c.Id() {
			continue
		}
		out = append(out, c)
	}
	return out
}
func literalArgs(n *tree_sitter.Node, s string) ([]string, bool) {
	var out []string
	for _, a := range methodArgs(n) {
		if a.Kind() != "string" {
			return nil, false
		}
		out = append(out, nodeText(a, s))
	}
	return out, true
}
func scalar(n *tree_sitter.Node) bool {
	if n == nil {
		return false
	}
	switch n.Kind() {
	case "string", "number", "boolean", "null":
		return true
	case "unary_expr":
		return scalar(n.ChildByFieldName("argument"))
	}
	return false
}
func pureExpression(n *tree_sitter.Node) bool {
	if n == nil {
		return false
	}
	switch n.Kind() {
	case "this_ref", "number", "string", "boolean", "null":
		return true
	case "identifier":
		return true
	case "field_access", "array_literal", "object_literal", "pair", "binary_expr", "unary_expr", "parenthesized_expr":
		for i := uint(0); i < n.NamedChildCount(); i++ {
			if !pureExpression(n.NamedChild(i)) {
				return false
			}
		}
		return true
	}
	return false
}
func (h *Handler) lintFindings(uri protocol.DocumentURI, text string) []lintFinding {
	return h.lintFindingsWithConfig(uri, text, h.workspaceConfig(uri))
}

func (h *Handler) lintFindingsWithConfig(uri protocol.DocumentURI, text string, cfg editorconfig.Config) []lintFinding {
	if !cfg.Lint.Enabled {
		return nil
	}
	tree, e := h.parser.Parse(string(uri)+":lint", text)
	if e != nil || tree == nil {
		return nil
	}
	defer tree.Close()
	if tree.RootNode().HasError() {
		return nil
	}
	var out []lintFinding
	lines := strings.Split(text, "\n")
	add := func(id, msg string, n *tree_sitter.Node, replacement string) {
		setting := cfg.Setting(id)
		if setting.Severity == "off" {
			return
		}
		// A suppression belongs to the following syntax node, including its
		// descendants. Wrapping that node must not re-enable its diagnostics.
		for target := n; target != nil && target.Kind() != "source"; target = target.Parent() {
			row := int(target.StartPosition().Row)
			if row == 0 {
				continue
			}
			previous := strings.TrimSpace(lines[row-1])
			if strings.HasPrefix(previous, "# bloblang-lint-disable-next-line ") {
				ids := strings.SplitN(strings.TrimPrefix(previous, "# bloblang-lint-disable-next-line "), " -- ", 2)[0]
				for _, disabled := range strings.Fields(strings.ReplaceAll(ids, ",", " ")) {
					if disabled == id || disabled == "all" {
						return
					}
				}
			}
		}
		severity := map[string]protocol.DiagnosticSeverity{"hint": protocol.SeverityHint, "info": protocol.SeverityInformation, "warn": protocol.SeverityWarning, "error": protocol.SeverityError}[setting.Severity]
		d := protocol.Diagnostic{Range: nodeLocation(uri, text, n).Range, Severity: &severity, Code: rawJSON(id), Source: "bloblang lint", Message: msg}
		out = append(out, lintFinding{d, replacement})
	}
	ss := symbols(tree.RootNode(), text)
	for i := range ss {
		d := &ss[i]
		if !d.declaration || d.kind != "variable" {
			continue
		}
		used := false
		for j := range ss {
			r := &ss[j]
			if !r.declaration && r.kind == "variable" {
				if v := visibleDefinition(ss, r); v != nil && v.node.Id() == d.node.Id() {
					used = true
					break
				}
			}
		}
		if !used {
			add("correctness/variables/no-unused-let", fmt.Sprintf("Local binding %q is never referenced.", d.name), d.node, "")
		}
	}
	walkNodes(tree.RootNode(), func(n *tree_sitter.Node) {
		if n.Kind() == "call_expr" && nodeText(n.ChildByFieldName("function"), text) == "env" {
			handled := false
			current := n
			for p := n.Parent(); p != nil && !benthosStatement(p.Kind()); p = p.Parent() {
				name := methodName(p, text)
				if name == "or" && p.ChildByFieldName("object") != nil && p.ChildByFieldName("object").StartByte() <= n.StartByte() && p.ChildByFieldName("object").EndByte() >= n.EndByte() {
					handled = true
				}
				if name == "catch" && p.ChildByFieldName("object") != nil && p.ChildByFieldName("object").StartByte() <= n.StartByte() && p.ChildByFieldName("object").EndByte() >= n.EndByte() && strings.Contains(nodeText(current, text), ".not_null(") {
					handled = true
				}
				if p.Kind() == "binary_expr" {
					op := nodeText(p.ChildByFieldName("operator"), text)
					if (op == "|" && p.ChildByFieldName("left") != nil && p.ChildByFieldName("left").StartByte() <= n.StartByte() && p.ChildByFieldName("left").EndByte() >= n.EndByte()) || op == "==" || op == "!=" {
						handled = true
					}
				}
				current = p
			}
			if !handled {
				binding := n
				for binding != nil && binding.Kind() != "let_assignment" && !benthosStatement(binding.Kind()) {
					binding = binding.Parent()
				}
				if binding != nil && binding.Kind() == "let_assignment" {
					name := binding.ChildByFieldName("name")
					for i := range ss {
						r := &ss[i]
						if r.declaration || r.kind != "variable" {
							continue
						}
						d := visibleDefinition(ss, r)
						if d == nil || name == nil || d.node.Id() != name.Id() {
							continue
						}
						for p := r.node.Parent(); p != nil && !benthosStatement(p.Kind()); p = p.Parent() {
							if p.Kind() == "binary_expr" {
								op := nodeText(p.ChildByFieldName("operator"), text)
								if (op == "==" || op == "!=") && (nodeText(p.ChildByFieldName("left"), text) == "null" || nodeText(p.ChildByFieldName("right"), text) == "null") {
									handled = true
								}
							}
						}
					}
				}
			}
			if !handled {
				add("correctness/environment/require-fallback", "An unset env() returns null; provide .or(default), .or(throw(...)), or .not_null().catch(...).", n, "")
			}
		}
		if methodName(n, text) == "without" {
			inner := n.ChildByFieldName("object")
			if methodName(inner, text) == "without" {
				a, ok := literalArgs(inner, text)
				b, ok2 := literalArgs(n, text)
				replacement := ""
				if ok && ok2 && !strings.Contains(nodeText(n, text), "#") {
					replacement = nodeText(inner.ChildByFieldName("object"), text) + ".without(" + strings.Join(append(a, b...), ", ") + ")"
				}
				add("style/objects/combine-without", "Combine consecutive without calls into one call.", n, replacement)
			}
		}
		if n.Kind() == "object_literal" {
			receiver := ""
			keys := []string{}
			good := true
			for i := uint(0); i < n.NamedChildCount(); i++ {
				p := n.NamedChild(i)
				if p.Kind() != "pair" {
					good = false
					break
				}
				k := nodeText(p.ChildByFieldName("key"), text)
				v := p.ChildByFieldName("value")
				if v == nil || v.Kind() != "field_access" {
					good = false
					break
				}
				field := nodeText(v.ChildByFieldName("field"), text)
				if strings.Trim(k, "\"`") != strings.Trim(field, "\"`") {
					good = false
					break
				}
				r := nodeText(v.ChildByFieldName("object"), text)
				if receiver != "" && receiver != r {
					good = false
					break
				}
				receiver = r
				keys = append(keys, k)
			}
			if good && len(keys) > 0 {
				add("style/objects/prefer-with", "Consider "+receiver+".with(...). Missing fields are omitted by with(), while this object retains null values; verify that distinction and receiver evaluation/errors.", n, "")
			}
		}
		if n.Kind() == "binary_expr" {
			left := n.ChildByFieldName("left")
			right := n.ChildByFieldName("right")
			if methodName(left, text) == "length" && nodeText(right, text) == "0" {
				filter := left.ChildByFieldName("object")
				if methodName(filter, text) == "filter" {
					op := nodeText(n.ChildByFieldName("operator"), text)
					if op == ">" || op == "!=" || op == "==" {
						add("style/arrays/prefer-any", "Consider any(predicate) for this existence check. any() short-circuits; verify receiver type and predicate errors or side effects.", n, "")
					}
				}
			}
		}
		// Analyze uninterrupted runs using direct AST statement siblings; comments stop runs.
		if n.Kind() == "source" || n.Kind() == "statement_block" {
			var run []*tree_sitter.Node
			seeded := false
			unknownState := n.Kind() != "source"
			if sample := h.getSample(uri); sample != nil && sample.HasRoot && n.Kind() == "source" {
				seeded = true
			}
			flush := func() {
				minimum := cfg.Setting("style/assignments/prefer-grouped").MinAssignments
				if minimum < 3 {
					minimum = 3
				}
				if unknownState && !seeded {
					if len(run) > 0 {
						seeded = true
					}
					run = nil
					return
				}
				if len(run) < minimum {
					if len(run) > 0 {
						seeded = true
					}
					run = nil
					return
				}
				keys := map[string]bool{}
				fields := []string{}
				safe := true
				for _, a := range run {
					var paths []string
					for i := uint(0); i < a.ChildCount(); i++ {
						c := a.Child(i)
						if a.FieldNameForChild(uint32(i)) == "path" {
							paths = append(paths, nodeText(c, text))
						}
					}
					v := a.ChildByFieldName("value")
					if len(paths) != 1 || keys[strings.Join(paths, ".")] || (!pureExpression(v) || strings.Contains(nodeText(v, text), "root.")) || (seeded && !scalar(v)) {
						safe = false
						break
					}
					keys[paths[0]] = true
					fields = append(fields, paths[0]+": "+nodeText(v, text))
				}
				if safe {
					msg := "Group these adjacent assignments into root = {...}."
					if seeded {
						msg = "Group these adjacent scalar assignments into root = root.assign({...})."
					}
					add("style/assignments/prefer-grouped", msg, run[0], "")
				}
				if len(run) > 0 {
					seeded = true
				}
				run = nil
			}
			for i := uint(0); i < n.NamedChildCount(); i++ {
				a := n.NamedChild(i)
				if a.Kind() == "root_assignment" {
					lhs := strings.TrimSpace(strings.SplitN(nodeText(a, text), "=", 2)[0])
					if lhs == "root" {
						flush()
						seeded = true
						continue
					}
					if strings.HasPrefix(lhs, "root.") {
						run = append(run, a)
						continue
					}
				}
				flush()
			}
			flush()
			// Copy followed by field deletion: advisory only, because type/error behavior differs.
			for i := uint(0); i+1 < n.NamedChildCount(); i++ {
				a, b := n.NamedChild(i), n.NamedChild(i+1)
				if a.Kind() == "root_assignment" && b.Kind() == "root_assignment" {
					lhs := strings.TrimSpace(strings.SplitN(nodeText(a, text), "=", 2)[0])
					blhs := strings.TrimSpace(strings.SplitN(nodeText(b, text), "=", 2)[0])
					if lhs == "root" && strings.HasPrefix(blhs, "root.") && nodeText(b.ChildByFieldName("value"), text) == "deleted()" {
						add("style/objects/prefer-without", "Consider copying the receiver with .without(...); verify its type and deletion behavior.", b, "")
					}
				}
			}
		}
	})
	return out
}
func (h *Handler) lintDiagnostics(uri protocol.DocumentURI, text string) []protocol.Diagnostic {
	var out []protocol.Diagnostic
	for _, f := range h.lintFindings(uri, text) {
		out = append(out, f.diagnostic)
	}
	return out
}
func (h *Handler) CodeAction(_ context.Context, p *protocol.CodeActionParams) ([]protocol.CodeAction, error) {
	uri := p.TextDocument.URI
	text, ok := h.documents.Text(uri)
	if !ok {
		return []protocol.CodeAction{}, nil
	}
	var out []protocol.CodeAction
	kind := protocol.CodeActionQuickFix
	appendFindings := func(local protocol.DocumentURI, source string, r *embeddedRegion) {
		for _, f := range h.lintFindings(local, source) {
			if f.replacement == "" {
				continue
			}
			rr := f.diagnostic.Range
			edit := protocol.TextEdit{Range: rr, NewText: f.replacement}
			if r != nil {
				rr = r.hostRange(text, rr)
				var valid bool
				edit, valid = r.sourceEdit(text, f.diagnostic.Range, f.replacement)
				if !valid {
					continue
				}
			}
			if rr.End.Line < p.Range.Start.Line || rr.Start.Line > p.Range.End.Line {
				continue
			}
			d := f.diagnostic
			d.Range = rr
			out = append(out, protocol.CodeAction{Title: d.Message, Kind: &kind, Diagnostics: []protocol.Diagnostic{d}, Edit: &protocol.WorkspaceEdit{Changes: map[protocol.DocumentURI][]protocol.TextEdit{uri: {edit}}}})
		}
	}
	if yamlDocument(uri) && !strings.Contains(string(uri), "#bloblang-") {
		for _, r := range h.regions(uri) {
			if r.style != 0 && r.style&yaml.LiteralStyle == 0 {
				continue
			}
			appendFindings(r.uri, r.text, &r)
		}
	} else {
		appendFindings(uri, text, nil)
	}
	return out, nil
}
