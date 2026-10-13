package lsp

import (
	"encoding/json"
	"strings"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"go.yaml.in/yaml/v3"
)

func sourceLineEnding(text string) string {
	if i := strings.IndexByte(text, '\n'); i > 0 && text[i-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

// Formatting changes the layout of code, not the scalar's chomping semantics.
func preserveScalarEnding(formatted, original string) string {
	return strings.TrimRight(formatted, "\r\n") + scalarBodySuffix(original)
}

func (r embeddedRegion) contentIndent(host string) int {
	for i, c := range []byte(r.text) {
		if c == '\n' || c == '\r' || i >= len(r.offsets) {
			continue
		}
		hostStart := strings.LastIndexByte(host[:r.offsets[i]], '\n') + 1
		localStart := strings.LastIndexByte(r.text[:i], '\n') + 1
		return r.offsets[i] - hostStart - (i - localStart)
	}
	return r.keyIndent + 2
}

// Keep the physical line ending and blank host lines after the last content
// line. In a chomped scalar these bytes are not present in its decoded value.
func scalarBodySuffix(body string) string {
	end := len(body)
	for end > 0 {
		start := strings.LastIndexByte(body[:end], '\n') + 1
		if strings.TrimSpace(body[start:end]) != "" {
			return body[end:]
		}
		if start == 0 {
			return body
		}
		end = start - 1
		if end > 0 && body[end-1] == '\r' {
			end--
		}
	}
	return body
}

func indentScalar(source string, indent int, eol string) string {
	lines := strings.Split(strings.TrimSuffix(source, scalarBodySuffix(source)), "\n")
	for i, line := range lines {
		lines[i] = strings.Repeat(" ", max(indent, 0)) + strings.TrimSuffix(line, "\r")
	}
	return strings.Join(lines, eol)
}

// replaceSource is shared by formatting and lint fixes. It encodes a decoded
// mapping back into its host scalar, preserving YAML properties and comments.
// The whole YAML tree is checked, not merely the edited Bloblang fragment.
func (r embeddedRegion) replaceSource(host, source string) (protocol.TextEdit, bool) {
	if r.interpolation || r.start < 0 || r.end > len(host) || r.scalarStart < r.start || r.scalarStart >= r.end {
		return protocol.TextEdit{}, false
	}
	properties := host[r.start:r.scalarStart]
	eol := sourceLineEnding(host)
	var replacement string
	if r.style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		newline := strings.IndexByte(host[r.scalarStart:r.end], '\n')
		if newline < 0 {
			return protocol.TextEdit{}, false
		}
		bodyStart := r.scalarStart + newline + 1
		header := strings.TrimSuffix(host[r.scalarStart:bodyStart-1], "\r")
		// A folded scalar cannot represent the formatter's explicit line breaks.
		header = "|" + header[1:]
		replacement = properties + header + eol + indentScalar(source, r.contentIndent(host), eol) + scalarBodySuffix(host[bodyStart:r.end])
	} else if strings.Contains(strings.TrimRight(source, "\r\n"), "\n") {
		indent := r.keyIndent + 2
		lineStart := strings.LastIndexByte(host[:r.scalarStart], '\n') + 1
		if strings.TrimSpace(host[lineStart:r.scalarStart]) == "" {
			indent = max(indent, r.scalarStart-lineStart+2)
		}
		replacement = properties + "|-" + eol + indentScalar(source, indent, eol)
	} else if r.style&yaml.SingleQuotedStyle != 0 {
		replacement = properties + "'" + strings.ReplaceAll(source, "'", "''") + "'"
	} else if r.style&yaml.DoubleQuotedStyle != 0 {
		encoded, _ := json.Marshal(source)
		replacement = properties + string(encoded)
	} else {
		replacement = properties + source
	}
	if !r.validReplacement(host, replacement, source) {
		// Quoting safely handles flow collections, plain scalars containing ': ',
		// and inline host comments that must not become Bloblang comments.
		encoded, _ := json.Marshal(source)
		replacement = properties + string(encoded)
		if !r.validReplacement(host, replacement, source) {
			return protocol.TextEdit{}, false
		}
	}
	return protocol.TextEdit{Range: protocol.Range{Start: bytePosition(host, r.start), End: bytePosition(host, r.end)}, NewText: replacement}, true
}

func (r embeddedRegion) validReplacement(host, replacement, source string) bool {
	var before, after yaml.Node
	if yaml.Unmarshal([]byte(host), &before) != nil || yaml.Unmarshal([]byte(host[:r.start]+replacement+host[r.end:]), &after) != nil {
		return false
	}
	starts := lineOffsets(host)
	lines := strings.Split(host, "\n")
	found := false
	var equivalent func(*yaml.Node, *yaml.Node) bool
	equivalent = func(a, b *yaml.Node) bool {
		if a.Kind != b.Kind || a.Tag != b.Tag || a.Anchor != b.Anchor || a.HeadComment != b.HeadComment || a.LineComment != b.LineComment || a.FootComment != b.FootComment || len(a.Content) != len(b.Content) {
			return false
		}
		target := a.Kind == yaml.ScalarNode && a.Line > 0 && a.Line <= len(starts) && starts[a.Line-1]+runeColumnByte(lines[a.Line-1], a.Column-1) == r.start
		if target {
			found = true
			if b.Value != source {
				return false
			}
		} else if a.Value != b.Value {
			return false
		}
		for i := range a.Content {
			if !equivalent(a.Content[i], b.Content[i]) {
				return false
			}
		}
		return true
	}
	return equivalent(&before, &after) && found
}

func (r embeddedRegion) sourceEdit(host string, local protocol.Range, replacement string) (protocol.TextEdit, bool) {
	start, end := positionByte(r.text, local.Start), positionByte(r.text, local.End)
	if start > end {
		return protocol.TextEdit{}, false
	}
	return r.replaceSource(host, r.text[:start]+replacement+r.text[end:])
}
