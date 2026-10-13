package lsp

import (
	protocol "github.com/owenrumney/go-lsp/lsp"
	"go.yaml.in/yaml/v3"
	"os"
	"strings"
)

type yamlPath struct {
	path       string
	start, end int
}

func externalMapping(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "from ") {
		return "", false
	}
	value := strings.TrimSpace(strings.TrimPrefix(s, "from "))
	if len(value) < 2 || (value[0] != '"' && value[0] != '\'' && value[0] != '`') {
		return "", false
	}
	return strings.Trim(value, "\"'`"), true
}
func yamlPaths(text string) []yamlPath {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(text), &doc) != nil {
		return nil
	}
	lines := strings.Split(text, "\n")
	starts := lineOffsets(text)
	var out []yamlPath
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				v := n.Content[i+1]
				if mappingKeys[n.Content[i].Value] && v.Kind == yaml.ScalarNode {
					if path, ok := externalMapping(v.Value); ok {
						_, offsets, _ := scalarSource(v, text, starts, lines)
						if len(offsets) > 0 {
							index := strings.Index(v.Value, path)
							out = append(out, yamlPath{path, offsets[index], offsets[min(index+len(path), len(offsets)-1)]})
						}
					}
				}
				walk(v)
			}
			return
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(&doc)
	return out
}
func (h *Handler) resolveMappingPath(uri protocol.DocumentURI, path string) string {
	return h.sourceContext(uri).MappingPath(path)
}
func (h *Handler) yamlPathDefinition(uri protocol.DocumentURI, text string, p protocol.Position) []protocol.Location {
	offset := positionByte(text, p)
	for _, path := range yamlPaths(text) {
		if offset >= path.start && offset <= path.end {
			return []protocol.Location{{URI: fileURI(h.resolveMappingPath(uri, path.path)), Range: protocol.Range{}}}
		}
	}
	return nil
}
func (h *Handler) yamlPathDiagnostics(uri protocol.DocumentURI, text string) []protocol.Diagnostic {
	var ds []protocol.Diagnostic
	severity := protocol.SeverityError
	for _, p := range yamlPaths(text) {
		resolved := h.resolveMappingPath(uri, p.path)
		if _, err := os.Stat(resolved); err != nil {
			ds = append(ds, protocol.Diagnostic{Range: protocol.Range{Start: bytePosition(text, p.start), End: bytePosition(text, p.end)}, Severity: &severity, Source: "bloblang import", Message: "Mapping file not found: " + resolved})
		}
	}
	return ds
}
