package lsp

import (
	"context"
	"os"
	"strings"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/redpanda-data/benthos/v4/public/bloblang"
	"github.com/teyfix/bloblang/internal/benthos"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

type formatToken struct {
	text                       string
	start, end                 uint
	trailingComma, inlineClose bool
}

func collection(n *tree_sitter.Node) bool {
	return n != nil && (n.Kind() == "object_literal" || n.Kind() == "array_literal")
}

func trailingCollectionComma(n *tree_sitter.Node) bool {
	if n.Kind() != "," || !collection(n.Parent()) {
		return false
	}
	next := n.NextSibling()
	for next != nil && next.Kind() == "comment" {
		next = next.NextSibling()
	}
	return next != nil && (next.Kind() == "}" || next.Kind() == "]")
}

func tokens(n *tree_sitter.Node, source []byte, out *[]formatToken) {
	switch n.Kind() {
	case "comment":
		// A CR consumed by the line-comment token belongs to the host line
		// ending, including when formatting adds the final newline.
		*out = append(*out, formatToken{text: strings.TrimSuffix(n.Utf8Text(source), "\r"), start: n.StartByte(), end: n.EndByte()})
		return
	case "string", "number":
		*out = append(*out, formatToken{text: n.Utf8Text(source), start: n.StartByte(), end: n.EndByte()})
		return
	}
	if n.ChildCount() == 0 {
		if n.EndByte() > n.StartByte() {
			parent := n.Parent()
			inlineClose := (n.Kind() == "}" || n.Kind() == "]") && collection(parent) && parent.StartPosition().Row == parent.EndPosition().Row
			*out = append(*out, formatToken{text: n.Utf8Text(source), start: n.StartByte(), end: n.EndByte(), trailingComma: trailingCollectionComma(n), inlineClose: inlineClose})
		}
		return
	}
	for i := uint(0); i < n.ChildCount(); i++ {
		tokens(n.Child(i), source, out)
	}
}

// Only a trailing collection comma immediately before a single-line closing
// delimiter may disappear. All other tokens, including comments, must match.
func equivalentFormatTokens(before, after []formatToken) bool {
	j := 0
	for _, token := range before {
		if j == len(after) {
			return false
		}
		if token.text == after[j].text {
			j++
		} else if !token.trailingComma || !after[j].inlineClose {
			return false
		}
	}
	return j == len(after)
}
func (h *Handler) formatText(uri protocol.DocumentURI, text string, tabSize int) (string, bool) {
	preview, err := benthos.PreviewEnvironment(h.benv, h.getSample(uri))
	if err != nil {
		return text, false
	}
	env := preview.WithCustomImporter(func(name string) ([]byte, error) { return os.ReadFile(h.resolveImportPath(uri, name)) })
	return h.formatTextWithEnvironment(uri, text, tabSize, env, h.workspaceConfig(uri).Formatter.PrintWidth)
}

func (h *Handler) formatTextWithEnvironment(uri protocol.DocumentURI, text string, tabSize int, env *bloblang.Environment, printWidth int) (string, bool) {
	_, originalErr := env.Parse(text)
	tree, err := h.parser.Parse(string(uri)+":format", text)
	if err != nil || tree == nil {
		return text, false
	}
	expressionOnly := false
	if tree.RootNode().HasError() && originalErr == nil {
		wrapped, e := h.parser.Parse(string(uri)+":format-expression", "root = "+text)
		if e == nil && wrapped != nil {
			if !wrapped.RootNode().HasError() {
				tree.Close()
				tree = wrapped
				text = "root = " + text
				expressionOnly = true
			} else {
				wrapped.Close()
			}
		}
	}
	defer tree.Close()
	if tree.RootNode().HasError() {

		return text, false
	}
	var ts []formatToken
	tokens(tree.RootNode(), []byte(text), &ts)
	if len(ts) == 0 {
		return text, true
	}
	if tabSize <= 0 {
		tabSize = 2
	}
	if tabSize > 8 {
		tabSize = 8
	}
	eol := sourceLineEnding(text)
	result := renderLayoutLineEnding(nodeLayout(tree.RootNode(), []byte(text), tabSize), printWidth, eol) + eol
	check, checkErr := h.parser.Parse(string(uri)+":formatted", result)
	if checkErr != nil || check == nil {
		return text, false
	}
	hasError := check.RootNode().HasError()
	var formattedTokens []formatToken
	tokens(check.RootNode(), []byte(result), &formattedTokens)
	check.Close()
	if hasError {
		return text, false
	}
	if !equivalentFormatTokens(ts, formattedTokens) {
		return text, false
	}
	// Reparse the formatted result before returning an edit.
	if _, err := env.Parse(result); err != nil && originalErr == nil {

		return text, false
	}
	if expressionOnly {
		result = strings.TrimPrefix(result, "root = ")
	}
	return result, true
}
func formatSpace(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if strings.HasPrefix(b, "#") {
		return true
	}
	if b == "." || a == "." || a == "$" || a == "@" || a == "!" || b == "," || b == ":" || b == ")" || b == "]" {
		return false
	}
	if b == "(" {
		if a == "if" || a == "match" {
			return true
		}
		return !validIdentifier(a)
	}
	if a == "(" || a == "[" {
		return false
	}
	if a == "{" && b == "}" {
		return false
	}
	if a == ":" || a == "," {
		return true
	}
	if b == "}" || a == "{" {
		return true
	}
	return true
}
func (h *Handler) Formatting(_ context.Context, p *protocol.DocumentFormattingParams) ([]protocol.TextEdit, error) {
	defer h.discardClosedDocumentTrees(hostDocumentURI(p.TextDocument.URI))
	text, ok := h.documents.Text(p.TextDocument.URI)
	if !ok {
		return []protocol.TextEdit{}, nil
	}
	return formatDocument(p.TextDocument.URI, text, p.Options.TabSize, h.formatText), nil
}

func formatDocument(uri protocol.DocumentURI, text string, tabSize int, format func(protocol.DocumentURI, string, int) (string, bool)) []protocol.TextEdit {
	if yamlDocument(uri) && !strings.Contains(string(uri), "#bloblang-") {
		var edits []protocol.TextEdit
		for _, r := range extractRegions(uri, text) {
			if r.interpolation {
				continue
			}
			formatted, valid := format(r.uri, r.text, tabSize)
			if !valid || formatted == r.text {
				continue
			}
			formatted = preserveScalarEnding(formatted, r.text)
			edit, ok := r.replaceSource(text, formatted)
			if ok && edit.NewText != text[r.start:r.end] {
				edits = append(edits, edit)
			}
		}
		return edits
	}
	formatted, valid := format(uri, text, tabSize)
	if !valid || formatted == text {
		return []protocol.TextEdit{}
	}
	return []protocol.TextEdit{{Range: protocol.Range{Start: protocol.Position{}, End: bytePosition(text, len(text))}, NewText: formatted}}
}
