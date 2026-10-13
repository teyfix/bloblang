package lsp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/dustin/go-humanize"
	protocol "github.com/owenrumney/go-lsp/lsp"
	pretty "github.com/teyfix/bloblang/internal/tidwall"
	"go.yaml.in/yaml/v3"
)

const (
	maxTooltipBytes       = 4096
	maxTooltipLines       = 60
	maxTooltipNodes       = 128
	maxTooltipDepth       = 8
	maxTooltipStringBytes = 512
)

func (h *Handler) preview(uri protocol.DocumentURI, raw string) (string, string) {
	c := h.workspaceConfig(uri)
	if c.Preview.Format == "json" {
		return "json", strings.TrimSpace(string(pretty.PrettyOptions([]byte(raw), &pretty.Options{Width: c.Formatter.PrintWidth, Indent: "  "})))
	}
	var n yaml.Node
	// YAML accepts JSON and retains numeric lexemes without float64 rounding.
	if yaml.Unmarshal([]byte(raw), &n) != nil {
		return "text", raw
	}
	var compact func(*yaml.Node, int)
	compact = func(n *yaml.Node, depth int) {
		n.Style = 0
		for _, ch := range n.Content {
			compact(ch, depth+1)
		}
		if n.Kind != yaml.MappingNode && n.Kind != yaml.SequenceNode {
			return
		}
		n.Style = yaml.FlowStyle
		b, e := yaml.Marshal(n)
		if e != nil || strings.Contains(strings.TrimSpace(string(b)), "\n") || len(strings.TrimSpace(string(b)))+2*depth > c.Formatter.PrintWidth {
			n.Style = 0
		}
	}
	compact(&n, 0)
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	if e.Encode(&n) != nil {
		return "text", raw
	}
	_ = e.Close()
	return "yaml", strings.TrimSpace(b.String())
}
func (h *Handler) previewMarkdown(uri protocol.DocumentURI, raw string) string {
	// Bound the value before YAML conversion: compact collection rendering can
	// otherwise serialize large subtrees repeatedly, even when the UI clips them.
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	truncated := false
	limited := raw
	if decoder.Decode(&value) == nil {
		budget := previewBudget{bytes: maxTooltipBytes, nodes: maxTooltipNodes}
		value = budget.clip(value, 0)
		truncated = budget.truncated
		if truncated {
			encoded, _ := json.Marshal(value)
			limited = string(encoded)
		}
	} else if len(raw) > maxTooltipBytes {
		limited = truncateUTF8(raw, maxTooltipBytes)
		truncated = true
	}
	lang, body := h.preview(uri, limited)
	// Escaping, indentation and multiline scalars can expand a bounded value.
	if len(body) > maxTooltipBytes {
		body = truncateUTF8(body, maxTooltipBytes)
		truncated = true
	}
	lines := strings.SplitN(body, "\n", maxTooltipLines+1)
	if len(lines) > maxTooltipLines {
		body = strings.Join(lines[:maxTooltipLines-1], "\n") + "\n…"
		truncated = true
	}
	// Sample strings can contain Markdown fences; never let them escape the
	// code block into active Markdown.
	fence := "```"
	for strings.Contains(body, fence) {
		fence += "`"
	}
	markdown := fence + lang + "\n" + body + "\n" + fence
	if truncated {
		markdown = fmt.Sprintf("**Preview truncated** (%s full value). Use **Show Input** / **Show Output** above the mapping to open the full message.\n\n", humanize.Bytes(uint64(len(raw)))) + markdown
	}
	return markdown
}

func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	limit = max(0, limit-len("…"))
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit] + "…"
}

type previewBudget struct {
	bytes, nodes int
	truncated    bool
}

func (b *previewBudget) clip(value any, depth int) any {
	if b.bytes <= 0 || b.nodes <= 0 {
		b.truncated = true
		return "…"
	}
	b.nodes--
	if depth >= maxTooltipDepth {
		b.truncated = true
		return "…"
	}
	b.bytes -= 4 // Collection delimiters, separators and scalar overhead.
	switch v := value.(type) {
	case string:
		limit := max(0, min(maxTooltipStringBytes, b.bytes))
		b.bytes -= min(len(v), limit)
		if len(v) > limit {
			b.truncated = true
		}
		return truncateUTF8(v, limit)
	case []any:
		out := make([]any, 0, min(len(v), b.nodes+1))
		for _, child := range v {
			if b.bytes <= 0 || b.nodes <= 0 {
				b.truncated = true
				out = append(out, "…")
				break
			}
			out = append(out, b.clip(child, depth+1))
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make(map[string]any)
		for _, key := range keys {
			if b.bytes <= 0 || b.nodes <= 0 || len(key) > min(maxTooltipStringBytes, b.bytes) {
				b.truncated = true
				break
			}
			b.bytes -= len(key)
			out[key] = b.clip(v[key], depth+1)
		}
		return out
	case json.Number:
		text := string(v)
		limit := max(0, min(maxTooltipStringBytes, b.bytes))
		b.bytes -= min(len(text), limit)
		if len(text) > limit {
			b.truncated = true
			return truncateUTF8(text, limit)
		}
		return value
	default:
		return value
	}
}
