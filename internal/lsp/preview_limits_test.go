package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/stretchr/testify/require"
)

func TestPreviewTooltipLimits(t *testing.T) {
	h, uri := featureHandler(t)
	fixtures := map[string]any{
		"large sample":        largePreviewFixture(),
		"long Unicode string": map[string]any{"text": strings.Repeat("🎬世界", 20000)},
		"many empty objects":  make([]any, 10000),
		"many lines":          map[string]any{"text": strings.Repeat("a\n", 200)},
		"deep":                map[string]any{"one": map[string]any{"two": map[string]any{"three": map[string]any{"four": map[string]any{"five": map[string]any{"six": map[string]any{"seven": map[string]any{"eight": map[string]any{"nine": "end"}}}}}}}}},
		"long key":            map[string]any{strings.Repeat("key", 2000): true},
	}
	for _, format := range []string{"yaml", "json"} {
		require.NoError(t, os.WriteFile(filepath.Join(h.workspaceRoot, ".bloblangrc.json"), []byte(`{"preview":{"format":"`+format+`"}}`), 0600))
		for name, value := range fixtures {
			t.Run(format+"/"+name, func(t *testing.T) {
				raw, err := json.Marshal(value)
				require.NoError(t, err)
				markdown := h.previewMarkdown(uri, string(raw))
				if format == "json" && name == "many lines" {
					require.NotContains(t, markdown, "Preview truncated", "JSON escapes newlines and fits without truncation")
				} else {
					require.True(t, strings.HasPrefix(markdown, "**Preview truncated**"), markdown)
					require.Contains(t, markdown, "Show Input")
					require.Contains(t, markdown, "Show Output")
				}
				require.Less(t, len(markdown), maxTooltipBytes+512)
				require.LessOrEqual(t, strings.Count(markdown, "\n"), maxTooltipLines+5)
				require.True(t, utf8.ValidString(markdown))
				require.Contains(t, markdown, "```"+format)
				after, err := json.Marshal(value)
				require.NoError(t, err)
				require.Equal(t, raw, after, "limiting must not alter the source value")
			})
		}
	}
}

func TestSmallPreviewAndFullPreviewRemainComplete(t *testing.T) {
	h, uri := featureHandler(t)
	raw := `{"id":9007199254740993,"name":"Ada"}`
	markdown := h.previewMarkdown(uri, raw)
	require.NotContains(t, markdown, "Preview truncated")
	require.Contains(t, markdown, "9007199254740993")
	require.Contains(t, markdown, "Ada")
	raw = `{"text":"` + strings.Repeat("long sample ", 1000) + `THE_END"}`
	markdown = h.previewMarkdown(uri, raw)
	require.Contains(t, markdown, "Preview truncated")
	require.NotContains(t, markdown, "THE_END")
	lang, body := h.preview(uri, raw)
	require.Equal(t, "yaml", lang)
	require.Contains(t, body, "THE_END")
	require.NotContains(t, body, "Preview truncated")
}

func TestPreviewFencesCannotBeClosedBySample(t *testing.T) {
	h, uri := featureHandler(t)
	raw, _ := json.Marshal(map[string]any{"text": "```\n[link](command:untrusted)\n```"})
	markdown := h.previewMarkdown(uri, string(raw))
	require.True(t, strings.HasPrefix(markdown, "````yaml\n"), markdown)
	require.True(t, strings.HasSuffix(markdown, "\n````"), markdown)
}

func TestLargeHoverAndInlayTooltipsAreBounded(t *testing.T) {
	h, uri := featureHandler(t)
	samplePath := filepath.Join(h.workspaceRoot, "large.sample.json")
	sample := map[string]any{"$bloblang": map[string]any{"input": largePreviewFixture()}}
	data, err := json.Marshal(sample)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(samplePath, data, 0600))
	t.Logf("sample size: %d bytes", len(data))
	text := "#!sample_from large.sample.json\nroot = this\n"
	openFeature(t, h, uri, text)
	hover := hoverAt(t, h, uri, text, "this")
	require.NotNil(t, hover)
	require.True(t, strings.HasPrefix(hover.Contents.Value(), "**Preview truncated**"))
	require.Less(t, len(hover.Contents.Value()), maxTooltipBytes+512)
	hints, err := h.InlayHint(context.Background(), &protocol.InlayHintParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Range: protocol.Range{End: protocol.Position{Line: 10000}}})
	require.NoError(t, err)
	require.Len(t, hints, 3)
	for _, hint := range hints {
		require.NotNil(t, hint.Tooltip)
		require.True(t, strings.HasPrefix(hint.Tooltip.Value, "**Preview truncated**"))
		require.Less(t, len(hint.Tooltip.Value), maxTooltipBytes+512)
	}
	lenses, err := h.CodeLens(context.Background(), &protocol.CodeLensParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}})
	require.NoError(t, err)
	count := 0
	for _, lens := range lenses {
		if lens.Command.Command != "bloblang-lsp.showResult" {
			continue
		}
		count++
		var full any
		require.NoError(t, json.Unmarshal(lens.Command.Arguments[0], &full))
		expected, _ := json.Marshal(largePreviewFixture())
		require.JSONEq(t, string(expected), string(lens.Command.Arguments[0]))
	}
	require.Equal(t, 2, count, "full input/output stay available")
}
