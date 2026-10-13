package lsp

import (
	"context"
	"strings"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/redpanda-data/benthos/v4/public/bloblang"
)

type signatureCall struct {
	delimiter     byte
	name          string
	method        bool
	argument      int
	argumentStart int
	parameterName string
}

// activeSignatureCall deliberately accepts incomplete expressions. A missing
// closing parenthesis is ordinary typing, but Tree-sitter can recover those
// expressions as a field access or identifier rather than a call node.
func activeSignatureCall(ctx context.Context, text string, offset int) (*signatureCall, error) {
	text = text[:min(max(offset, 0), len(text))]
	var stack []signatureCall
	last, previous := "", ""
	nextCancelCheck := 0
	token := func(value string) { previous, last = last, value }
	for i := 0; i < len(text); {
		if i >= nextCancelCheck {
			nextCancelCheck = i + 4096
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		ch := text[i]
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			i++
			continue
		}
		if ch == '#' {
			end := strings.IndexByte(text[i:], '\n')
			if end < 0 {
				return nil, nil
			}
			i += end + 1
			continue
		}
		if ch == '"' {
			if strings.HasPrefix(text[i:], `"""`) {
				i += 3
				end := strings.Index(text[i:], `"""`)
				if end < 0 {
					break
				}
				i += end + 3
			} else {
				i++
				for i < len(text) {
					if text[i] == '\\' {
						i = min(i+2, len(text))
						continue
					}
					if text[i] == '"' {
						i++
						break
					}
					i++
				}
			}
			token(`"`)
			continue
		}
		if isIdentChar(ch) && !(ch >= '0' && ch <= '9') {
			start := i
			for i < len(text) && isIdentChar(text[i]) {
				i++
			}
			token(text[start:i])
			continue
		}
		switch ch {
		case '(', '[', '{':
			frame := signatureCall{delimiter: ch, argumentStart: i + 1}
			if ch == '(' && validIdentifier(last) && previous != "$" && previous != "@" {
				frame.name, frame.method = last, previous == "."
			}
			stack = append(stack, frame)
		case ')', ']', '}':
			want := byte('(')
			if ch == ']' {
				want = '['
			} else if ch == '}' {
				want = '{'
			}
			if len(stack) > 0 && stack[len(stack)-1].delimiter == want {
				stack = stack[:len(stack)-1]
			} else {
				stack = nil
			}
		case ',':
			if len(stack) > 0 {
				frame := &stack[len(stack)-1]
				if frame.delimiter == '(' {
					frame.argument++
					frame.argumentStart = i + 1
					frame.parameterName = ""
				}
			}
		case ':':
			if len(stack) > 0 {
				frame := &stack[len(stack)-1]
				name := strings.TrimSpace(text[frame.argumentStart:i])
				if frame.delimiter == '(' && validIdentifier(name) {
					frame.parameterName = name
				}
			}
		}
		token(string(ch))
		i++
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].name != "" {
			return &stack[i], nil
		}
	}
	return nil, ctx.Err()
}

func (h *Handler) SignatureHelp(ctx context.Context, p *protocol.SignatureHelpParams) (*protocol.SignatureHelp, error) {
	defer h.discardClosedDocumentTrees(hostDocumentURI(p.TextDocument.URI))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	uri := p.TextDocument.URI
	position := p.Position
	if yamlDocument(uri) && !strings.Contains(string(uri), "#bloblang-") {
		region, local, ok := h.regionAt(uri, position)
		if !ok {
			return nil, nil
		}
		uri, position = region.uri, local
	}
	text, ok := h.documents.Text(uri)
	if !ok {
		return nil, nil
	}
	call, err := activeSignatureCall(ctx, text, positionByte(text, position))
	if err != nil || call == nil {
		return nil, err
	}
	var params bloblang.TemplateParamsData
	description := ""
	found := false
	if call.method {
		h.benv.WalkMethods(func(name string, view *bloblang.MethodView) {
			if name == call.name {
				data := view.TemplateData()
				params, description, found = data.Params, data.Description, true
			}
		})
	} else {
		h.benv.WalkFunctions(func(name string, view *bloblang.FunctionView) {
			if name == call.name {
				data := view.TemplateData()
				params, description, found = data.Params, data.Description, true
			}
		})
	}
	if !found {
		return nil, nil
	}
	signature := protocol.SignatureInformation{Documentation: &protocol.MarkupContent{Kind: protocol.MarkupKind("markdown"), Value: description}}
	labels := []string{}
	active := call.argument
	for i, param := range params.Definitions {
		label := param.Name
		if param.IsOptional && param.DefaultMarshalled == "" {
			label += "?"
		}
		if param.ValueType != "" {
			label += ": " + param.ValueType
		}
		if param.DefaultMarshalled != "" {
			label += " = " + param.DefaultMarshalled
		}
		labels = append(labels, label)
		signature.Parameters = append(signature.Parameters, protocol.ParameterInformation{Label: label, Documentation: &protocol.MarkupContent{Kind: protocol.MarkupKind("markdown"), Value: param.Description}})
		if call.parameterName == param.Name {
			active = i
		}
	}
	if params.Variadic {
		labels = append(labels, "...args")
		signature.Parameters = append(signature.Parameters, protocol.ParameterInformation{Label: "...args"})
	}
	signature.Label = call.name + "(" + strings.Join(labels, ", ") + ")"
	result := &protocol.SignatureHelp{Signatures: []protocol.SignatureInformation{signature}, ActiveSignature: new(0)}
	if len(signature.Parameters) > 0 {
		result.ActiveParameter = new(min(active, len(signature.Parameters)-1))
	}
	return result, nil
}
