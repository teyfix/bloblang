package lsp

import (
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"strings"
	"unicode/utf8"
)

// layout groups choose a flat representation if it fits the remaining columns.
// Hard lines separate statements and keep line comments from swallowing code.
type layout struct {
	text              string
	parts             []layout
	line, hard, group bool
	brokenOnly        bool
	indent            int
}

func literal(s string) layout          { return layout{text: s} }
func soft(s string) layout             { return layout{text: s, line: true} }
func hardline() layout                 { return layout{line: true, hard: true} }
func concat(ds ...layout) layout       { return layout{parts: ds} }
func nested(d layout, size int) layout { return layout{parts: []layout{d}, indent: size} }
func grouped(d layout) layout          { return layout{parts: []layout{d}, group: true} }
func flatLayout(d layout) (string, bool) {
	if d.brokenOnly {
		return "", true
	}
	if d.hard {
		return "", false
	}
	if d.line || d.parts == nil {
		return d.text, !strings.Contains(d.text, "\n")
	}
	var b strings.Builder
	for _, p := range d.parts {
		s, ok := flatLayout(p)
		if !ok {
			return "", false
		}
		b.WriteString(s)
	}
	return b.String(), true
}

// Reserve the following punctuation/text up to its first break opportunity.
// In particular, a hugged object's fit must include the call's closing ')'.
func layoutPrefixWidth(d layout) (int, bool) {
	if d.line {
		return 0, false
	}
	if d.parts == nil {
		if i := strings.IndexByte(d.text, '\n'); i >= 0 {
			return utf8.RuneCountInString(d.text[:i]), false
		}
		return utf8.RuneCountInString(d.text), true
	}
	width := 0
	for _, p := range d.parts {
		n, complete := layoutPrefixWidth(p)
		width += n
		if !complete {
			return width, false
		}
	}
	return width, true
}

func renderLayout(d layout, width int) string {
	var b strings.Builder
	column := 0
	var render func(layout, int, bool, int)
	render = func(d layout, indent int, flat bool, following int) {
		indent += d.indent
		if d.group && !flat {
			if s, ok := flatLayout(d); ok && column+utf8.RuneCountInString(s)+following <= width {
				b.WriteString(s)
				column += utf8.RuneCountInString(s)
				return
			}
		}
		if d.line {
			if flat && !d.hard {
				b.WriteString(d.text)
				column += utf8.RuneCountInString(d.text)
			} else {
				b.WriteByte('\n')
				b.WriteString(strings.Repeat(" ", indent))
				column = indent
			}
			return
		}
		if d.parts == nil {
			b.WriteString(d.text)
			if i := strings.LastIndex(d.text, "\n"); i >= 0 {
				column = utf8.RuneCountInString(d.text[i+1:])
			} else {
				column += utf8.RuneCountInString(d.text)
			}
			return
		}
		suffixes := make([]int, len(d.parts))
		for i := len(d.parts) - 1; i >= 0; i-- {
			suffixes[i] = following
			prefix, complete := layoutPrefixWidth(d.parts[i])
			if complete {
				following += prefix
			} else {
				following = prefix
			}
		}
		for i, p := range d.parts {
			render(p, indent, flat, suffixes[i])
		}
	}
	render(d, 0, false, 0)
	return b.String()
}
func edgeTokens(n *tree_sitter.Node, source []byte) (string, string) {
	var ts []formatToken
	tokens(n, source, &ts)
	if len(ts) == 0 {
		return "", ""
	}
	return ts[0].text, ts[len(ts)-1].text
}
func nodeLayout(n *tree_sitter.Node, source []byte, size int) layout {
	if n.ChildCount() == 0 || n.Kind() == "string" || n.Kind() == "comment" || n.Kind() == "number" {
		return literal(n.Utf8Text(source))
	}
	if n.Kind() == "method_call" {
		return chainLayout(n, source, size)
	}
	if n.Kind() == "call_expr" {
		return callLayout(n, source, size)
	}
	var children []*tree_sitter.Node
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c.EndByte() > c.StartByte() {
			children = append(children, c)
		}
	}
	if len(children) == 0 {
		return literal("")
	}
	kind := n.Kind()
	list := kind == "object_literal" || kind == "array_literal" || kind == "parenthesized_expr" || kind == "call_expr" || kind == "method_call"
	block := kind == "statement_block" || kind == "map_declaration" || kind == "match_expr" || kind == "if_expr"
	var parts []layout
	for i, c := range children {
		if i > 0 {
			prev := children[i-1]
			_, last := edgeTokens(prev, source)
			first, _ := edgeTokens(c, source)
			gap := string(source[prev.EndByte():c.StartByte()])
			separator := literal("")
			if kind == "source" {
				separator = hardline()
				if strings.Count(gap, "\n") > 1 {
					separator = concat(hardline(), hardline())
				}
			} else if prev.Kind() == "comment" {
				separator = hardline()
			} else if block && (last == "{" || first == "}" || benthosStatement(c.Kind()) || c.Kind() == "match_case") {
				separator = hardline()
			} else if list && last == "," && first != "}" && first != "]" {
				separator = soft(" ")
			} else if list && (last == "{" || last == "[" || last == "(") {
				flat := ""
				if last == "{" && first != "}" {
					flat = " "
				}
				separator = soft(flat)
			} else if list && (first == "}" || first == "]" || first == ")") {
				flat := ""
				if first == "}" && last != "{" {
					flat = " "
				}
				separator = soft(flat)
			} else if kind == "binary_expr" && i == 2 {
				separator = soft(" ")
			} else if kind == "unary_expr" {
				separator = literal("")
			} else if c.Kind() == "comment" {
				separator = literal(" ")
				if strings.Contains(gap, "\n") {
					separator = hardline()
				}
			} else if !list && strings.Contains(gap, "\n") && kind != "binary_expr" && kind != "field_access" {
				separator = hardline()
			} else if formatSpace(last, first) {
				separator = literal(" ")
			}
			// Closing delimiters align with the group; interiors indent after opening.
			if (list || block) && (first == "}" || first == "]" || first == ")") {
				parts = append(parts, separator)
			} else if list || block {
				parts = append(parts, nested(separator, size))
			} else {
				parts = append(parts, separator)
			}
		}
		d := nodeLayout(c, source, size)
		if trailingCollectionComma(c) {
			d.brokenOnly = true
		}
		first, _ := edgeTokens(c, source)
		elseIf := kind == "if_expr" && c.Kind() == "if_expr" && i > 0 && children[i-1].Kind() == "else"
		if (list || block) && i > 0 && first != "}" && first != "]" && first != ")" && !elseIf {
			d = nested(d, size)
		}
		parts = append(parts, d)
	}
	d := concat(parts...)
	if list || kind == "binary_expr" || kind == "field_access" || kind == "unary_expr" {
		d = grouped(d)
	}
	return d
}

// A call owns only its arguments, never its receiver. Including the receiver in
// this group made a long chain expand even tiny calls such as join("; ").
func callLayout(n *tree_sitter.Node, source []byte, size int) layout {
	name := n.ChildByFieldName("method")
	if name == nil {
		name = n.ChildByFieldName("function")
	}
	var args []layout
	var previous *tree_sitter.Node
	lambda := false
	argumentCount := 0
	var onlyArgument *tree_sitter.Node
	inside := false
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c.Kind() == "(" {
			inside = true
			continue
		}
		if !inside || c.Kind() == ")" {
			continue
		}
		if previous != nil {
			switch {
			case previous.Kind() == "comment":
				args = append(args, hardline())
			case c.Kind() == "comment":
				if strings.Contains(string(source[previous.EndByte():c.StartByte()]), "\n") {
					args = append(args, hardline())
				} else {
					args = append(args, literal(" "))
				}
			case previous.Kind() == ",":
				args = append(args, soft(" "))
			}
		}
		lambda = lambda || c.Kind() == "lambda"
		if c.IsNamed() && c.Kind() != "comment" {
			argumentCount++
			onlyArgument = c
		}
		if c.Kind() == "lambda" && c.ChildCount() == 3 {
			// Fit the preceding arguments and lambda parameter independently of
			// the body, so a multiline body does not split a short call header.
			args = append(args, literal(c.ChildByFieldName("param").Utf8Text(source)+" -> "))
			args = []layout{grouped(concat(args...)), nodeLayout(c.ChildByFieldName("body"), source, size)}
		} else {
			args = append(args, nodeLayout(c, source, size))
		}
		previous = c
	}
	start := literal(name.Utf8Text(source) + "(")
	if len(args) == 0 {
		return concat(start, literal(")"))
	}
	opening := soft("")
	argumentIndent := size
	hug := argumentCount == 1 && collection(onlyArgument) && previous == onlyArgument
	if lambda || hug {
		// Lambdas and sole collection arguments supply their own break points
		// and indentation, keeping their opening beside the method name.
		opening = literal("")
		argumentIndent = 0
	}
	closing := soft("")
	if hug {
		closing = literal("")
	}
	if previous.Kind() == "lambda" {
		switch previous.ChildByFieldName("body").Kind() {
		case "object_literal", "array_literal", "if_expr", "match_expr":
			// The body's closing delimiter also closes the lambda visually.
			closing = literal("")
		}
	}
	if previous.Kind() == "comment" {
		closing = hardline()
	}
	return grouped(concat(start, nested(concat(opening, concat(args...)), argumentIndent), closing, literal(")")))
}

func chainLayout(n *tree_sitter.Node, source []byte, size int) layout {
	var calls []*tree_sitter.Node
	receiver := n
	for receiver.Kind() == "method_call" {
		calls = append(calls, receiver)
		receiver = receiver.ChildByFieldName("object")
	}
	parts := []layout{nodeLayout(receiver, source, size)}
	for i := len(calls) - 1; i >= 0; i-- {
		call := calls[i]
		method := call.ChildByFieldName("method")
		object := call.ChildByFieldName("object")
		parts = append(parts, literal("."))
		var comments []layout
		for j := uint(0); j < call.ChildCount(); j++ {
			c := call.Child(j)
			if c.Kind() == "comment" && c.StartByte() >= object.EndByte() && c.EndByte() <= method.StartByte() {
				comments = append(comments, hardline(), literal(c.Utf8Text(source)))
			}
		}
		if len(comments) > 0 {
			parts = append(parts, nested(concat(comments...), size), nested(hardline(), size))
		} else {
			parts = append(parts, nested(soft(""), size))
		}
		parts = append(parts, nested(callLayout(call, source, size), size))
	}
	return grouped(concat(parts...))
}
