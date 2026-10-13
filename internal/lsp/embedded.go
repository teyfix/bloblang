package lsp

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"go.yaml.in/yaml/v3"
)

type embeddedRegion struct {
	uri               protocol.DocumentURI
	text              string
	offsets           []int
	start, end        int
	style             yaml.Style
	indent            int
	interpolation     bool
	samplePath        string
	sampleNumber      int
	keyRow, keyIndent int
}

func yamlDocument(uri protocol.DocumentURI) bool {
	s := strings.ToLower(strings.Split(string(uri), "#")[0])
	return strings.HasSuffix(s, ".yaml") || strings.HasSuffix(s, ".yml")
}

var mappingKeys = map[string]bool{"mapping": true, "request_map": true, "result_map": true, "args_mapping": true, "fields_mapping": true, "check": true, "bloblang": true}

func lineOffsets(text string) []int {
	out := []int{0}
	for i, c := range []byte(text) {
		if c == '\n' {
			out = append(out, i+1)
		}
	}
	return out
}
func extractRegions(uri protocol.DocumentURI, text string) []embeddedRegion {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(text), &doc) != nil {
		return nil
	}
	starts := lineOffsets(text)
	lines := strings.Split(text, "\n")
	var out []embeddedRegion
	var walk func(*yaml.Node, bool)
	walk = func(n *yaml.Node, mapping bool) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				before := len(out)
				walk(n.Content[i+1], mappingKeys[n.Content[i].Value])
				for j := before; j < len(out) && n.Content[i+1].Kind == yaml.ScalarNode; j++ {
					out[j].keyRow = n.Content[i].Line - 1
					out[j].keyIndent = n.Content[i].Column - 1
				}
			}
			return
		}
		if n.Kind == yaml.ScalarNode && n.Tag == "!!str" {
			decoded, offsets, end := scalarSource(n, text, starts, lines)
			if decoded == "" {
				return
			}
			start := starts[n.Line-1] + runeColumnByte(lines[n.Line-1], n.Column-1)
			if mapping {
				if _, external := externalMapping(decoded); external {
					return
				}
				out = append(out, embeddedRegion{text: decoded, offsets: offsets, start: start, end: end, style: n.Style, indent: n.Column - 1})
				return
			}
			for _, span := range interpolationSpans(decoded) {
				body := decoded[span[0]:span[1]]
				prefix := "root = "
				mapped := make([]int, len(prefix))
				for i := range mapped {
					mapped[i] = offsets[span[0]]
				}
				mapped = append(mapped, offsets[span[0]:span[1]+1]...)
				out = append(out, embeddedRegion{text: prefix + body, offsets: mapped, start: offsets[span[0]], end: offsets[span[1]], interpolation: true})
			}
			return
		}
		for _, ch := range n.Content {
			walk(ch, false)
		}
	}
	walk(&doc, false)
	number := 0
	for i := range out {
		if !out[i].interpolation {
			number++
			out[i].sampleNumber = number
			row := out[i].keyRow - 1
			if row >= 0 {
				line := lines[row]
				indent := len(line) - len(strings.TrimLeft(line, " "))
				trim := strings.TrimSpace(line)
				if indent == out[i].keyIndent && strings.HasPrefix(trim, "# bloblang-sample:") {
					out[i].samplePath = strings.TrimSpace(strings.TrimPrefix(trim, "# bloblang-sample:"))
				}
			}
		}
		out[i].uri = protocol.DocumentURI(fmt.Sprintf("%s#bloblang-%d", uri, i))
	}
	return out
}
func scalarSource(n *yaml.Node, text string, starts []int, lines []string) (string, []int, int) {
	row := n.Line - 1
	start := starts[row] + runeColumnByte(lines[row], n.Column-1)
	var decoded strings.Builder
	var positions []int
	appendPart := func(s string, offset int) {
		decoded.WriteString(s)
		for i := 0; i < len(s); i++ {
			positions = append(positions, offset+i)
		}
	}
	if n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		last := row + 1
		indent := -1
		for last < len(lines) {
			line := lines[last]
			if strings.TrimSpace(line) == "" {
				last++
				continue
			}
			spaces := len(line) - len(strings.TrimLeft(line, " "))
			if spaces <= len(lines[row])-len(strings.TrimLeft(lines[row], " ")) || (indent >= 0 && spaces < indent) {
				break
			}
			if indent < 0 {
				indent = spaces
			}
			last++
		}
		if indent < 0 {
			indent = n.Column + 1
		}
		for r := row + 1; r < last; r++ {
			line := lines[r]
			col := indent
			if col > len(line) {
				col = len(line)
			}
			appendPart(line[col:], starts[r]+col)
			if r+1 < last {
				sep := "\n"
				if n.Style&yaml.FoldedStyle != 0 && line[col:] != "" && strings.TrimSpace(lines[r+1]) != "" && len(lines[r+1])-len(strings.TrimLeft(lines[r+1], " ")) == indent {
					sep = " "
				}
				appendPart(sep, starts[r]+len(line))
			}
		}
		end := len(text)
		if last < len(starts) {
			end = starts[last]
		}
		d := decoded.String()
		if strings.HasSuffix(n.Value, "\n") && !strings.HasSuffix(d, "\n") {
			appendPart("\n", end-1)
			d = decoded.String()
		}
		positions = append(positions, end)
		if d != n.Value { // Keep YAML's decoded content authoritative. Align differing trailing line breaks.
			if strings.TrimRight(d, "\n") == strings.TrimRight(n.Value, "\n") {
				d = n.Value
				for len(positions) < len(d)+1 {
					positions = append(positions, end)
				}
				positions = positions[:len(d)+1]
			} else {
				mapped, ok := alignScalar(d, n.Value, positions)
				if !ok {
					return "", nil, end
				}
				positions = mapped
				d = n.Value
			}
		}
		// A chomped scalar ends before its physical newline. Mapping its final
		// boundary to the next host line would make a quick fix consume that
		// newline and join the following YAML key to the replacement.
		if len(d) > 0 && !strings.HasSuffix(d, "\n") {
			positions[len(d)] = positions[len(d)-1] + 1
		}
		return d, positions, end
	}
	if n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle) != 0 {
		quote := text[start]
		i := start + 1
		for i < len(text) {
			if text[i] == quote {
				if quote == '\'' && i+1 < len(text) && text[i+1] == quote {
					appendPart("'", i)
					i += 2
					continue
				}
				i++
				break
			}
			if quote == '"' && text[i] == '\\' && i+1 < len(text) {
				begin := i
				i += 2
				if text[begin+1] == 'u' {
					i = begin + 6
				} else if text[begin+1] == 'U' {
					i = begin + 10
				} else if text[begin+1] == 'x' {
					i = begin + 4
				}
				if i > len(text) {
					return "", nil, start
				}
				esc := text[begin:i]
				var s string
				err := yaml.Unmarshal([]byte("\""+esc+"\""), &s)
				if err != nil {
					return "", nil, start
				}
				decoded.WriteString(s)
				for range []byte(s) {
					positions = append(positions, begin)
				}
				continue
			}
			_, size := utf8.DecodeRuneInString(text[i:])
			appendPart(text[i:i+size], i)
			i += size
		}
		positions = append(positions, i-1)
		if decoded.String() != n.Value {
			mapped, ok := alignScalar(decoded.String(), n.Value, positions)
			if !ok {
				return "", nil, i
			}
			positions = mapped
		}
		return n.Value, positions, i
	}
	// Plain scalar content commonly stays on one line. YAML folds multiline plain
	// scalars; derive position mapping from decoded words in source order.
	i := start
	for _, r := range n.Value {
		if r == ' ' || r == '\n' {
			for i < len(text) && (text[i] == ' ' || text[i] == '\n' || text[i] == '\r') {
				i++
			}
			decoded.WriteRune(r)
			positions = append(positions, i)
			continue
		}
		s := string(r)
		idx := strings.Index(text[i:], s)
		if idx < 0 {
			return "", nil, start
		}
		i += idx
		appendPart(s, i)
		i += len(s)
	}
	positions = append(positions, i)
	return decoded.String(), positions, i
}
func interpolationSpans(s string) [][2]int {
	var out [][2]int
	for i := 0; i < len(s); i++ {
		if !strings.HasPrefix(s[i:], "${!") {
			continue
		}
		start := i + 3
		depth := 0
		quote := byte(0)
		for j := start; j < len(s); j++ {
			c := s[j]
			if quote != 0 {
				if c == '\\' {
					j++
					continue
				}
				if c == quote {
					quote = 0
				}
				continue
			}
			if c == '"' || c == '\'' || c == '`' {
				quote = c
				continue
			}
			if c == '{' {
				depth++
			}
			if c == '}' {
				if depth == 0 {
					out = append(out, [2]int{start, j})
					i = j
					break
				}
				depth--
			}
		}
	}
	return out
}
func (r embeddedRegion) localPosition(host string, p protocol.Position) (protocol.Position, bool) {
	b := positionByte(host, p)
	if b < r.start || b > r.end {
		return protocol.Position{}, false
	}
	i := sort.Search(len(r.offsets), func(i int) bool { return r.offsets[i] >= b })
	if i >= len(r.offsets) {
		i = len(r.offsets) - 1
	}
	return bytePosition(r.text, i), true
}
func (r embeddedRegion) hostPosition(host string, p protocol.Position) protocol.Position {
	b := positionByte(r.text, p)
	if b >= len(r.offsets) {
		b = len(r.offsets) - 1
	}
	if b < 0 {
		b = 0
	}
	return bytePosition(host, r.offsets[b])
}
func (r embeddedRegion) hostRange(host string, rr protocol.Range) protocol.Range {
	return protocol.Range{Start: r.hostPosition(host, rr.Start), End: r.hostPosition(host, rr.End)}
}
func (h *Handler) regions(uri protocol.DocumentURI) []embeddedRegion {
	text, ok := h.documents.Text(uri)
	if !ok || !yamlDocument(uri) || strings.Contains(string(uri), "#bloblang-") {
		return nil
	}
	return extractRegions(uri, text)
}
func (h *Handler) syncRegions(uri protocol.DocumentURI, text string) {
	if !yamlDocument(uri) || strings.Contains(string(uri), "#bloblang-") {
		return
	}
	h.clearRegions(uri)
	for _, r := range extractRegions(uri, text) {
		_, _ = h.documents.Open(&protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{URI: r.uri, LanguageID: "bloblang", Text: r.text}})
		h.updateRegionSample(uri, r)
	}
}
func (h *Handler) regionAt(uri protocol.DocumentURI, p protocol.Position) (embeddedRegion, protocol.Position, bool) {
	text, _ := h.documents.Text(uri)
	for _, r := range h.regions(uri) {
		if pos, ok := r.localPosition(text, p); ok {
			return r, pos, true
		}
	}
	return embeddedRegion{}, protocol.Position{}, false
}

func runeColumnByte(s string, column int) int {
	if column <= 0 {
		return 0
	}
	n := 0
	for i := range s {
		if n == column {
			return i
		}
		n++
	}
	return len(s)
}
func spaceByte(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
func alignScalar(raw, decoded string, offsets []int) ([]int, bool) {
	var out []int
	i := 0
	for j := 0; j < len(decoded); {
		if spaceByte(decoded[j]) {
			start := i
			for i < len(raw) && spaceByte(raw[i]) {
				i++
			}
			mapped := offsets[min(start, len(offsets)-1)]
			for j < len(decoded) && spaceByte(decoded[j]) {
				out = append(out, mapped)
				j++
			}
			continue
		}
		for i < len(raw) && spaceByte(raw[i]) {
			i++
		}
		if i >= len(raw) || raw[i] != decoded[j] {
			return nil, false
		}
		out = append(out, offsets[i])
		i++
		j++
	}
	out = append(out, offsets[min(i, len(offsets)-1)])
	return out, true
}

func (h *Handler) clearRegions(uri protocol.DocumentURI) {
	prefix := string(uri) + "#bloblang-"
	for _, v := range h.documents.URIs() {
		if !strings.HasPrefix(string(v), prefix) {
			continue
		}
		h.documents.Close(&protocol.DidCloseTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: v}})
		h.samplesMu.Lock()
		delete(h.samples, v)
		h.samplesMu.Unlock()
		h.sampleDiagnosticsMu.Lock()
		delete(h.sampleDiagnostics, v)
		h.sampleDiagnosticsMu.Unlock()
		h.parser.InvalidateDocument(string(v))
	}
}
