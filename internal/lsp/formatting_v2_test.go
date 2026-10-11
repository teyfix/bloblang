package lsp

import (
	"context"
	"fmt"
	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWidthFormattingUnaryAndComments(t *testing.T) {
	h, uri := featureHandler(t)
	require.NoError(t, os.WriteFile(filepath.Join(h.workspaceRoot, ".bloblangrc.json"), []byte(`{"formatter":{"printWidth":40}}`), 0600))
	for _, text := range []string{
		"root = this.items.index( -1 )\nroot.neg = -this.value\nroot.bad = !errored()\n",
		"root = {foo: this.foo, bar: this.bar, baz: this.baz, qux: this.qux}\n",
		"root = [1, # important\n 2, 3]\n",
		"root = this.foo.replace(\"a fairly long argument\", \"another long argument\")\n",
		"if this.ok { root.foo = 1\nroot.bar = 2 } else { root = deleted() }\n",
	} {
		formatted, ok := h.formatText(uri, text, 2)
		require.True(t, ok, text)
		t.Log(formatted)
		twice, ok := h.formatText(uri, formatted, 2)
		require.True(t, ok)
		require.Equal(t, formatted, twice)
		if strings.Contains(text, "index") {
			require.Contains(t, formatted, "index(-1)")
			require.Contains(t, formatted, "-this.value")
			require.Contains(t, formatted, "!errored()")
		} else if strings.Contains(text, "qux") {
			require.Contains(t, formatted, "\n  foo:")
		}
	}
	text := "root = {\nfoo: 1,\nbar: 2\n}"
	formatted, ok := h.formatText(uri, text, 2)
	require.True(t, ok)
	require.Equal(t, "root = { foo: 1, bar: 2 }\n", formatted)
}

func TestWrappedYAMLScalarRemainsValid(t *testing.T) {
	h, _ := featureHandler(t)
	require.NoError(t, os.WriteFile(filepath.Join(h.workspaceRoot, ".bloblangrc.json"), []byte(`{"formatter":{"printWidth":30}}`), 0600))
	uri := fileURI(filepath.Join(h.workspaceRoot, "config.yaml"))
	for _, text := range []string{"mapping: 'root = {foo: this.foo, bar: this.bar, baz: this.baz}'\nnext: preserved\n", "mapping:\n  'root = {foo: this.foo, bar: this.bar, baz: this.baz}'\nnext: preserved\n", "mapping: root={foo:1,bar:2}\nnext: preserved\n", "check: '!errored() && this.foo == \"a fairly long string\"'\n"} {
		openFeature(t, h, uri, text)
		p := &protocol.DocumentFormattingParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Options: protocol.FormattingOptions{TabSize: 2}}
		edits, e := h.Formatting(context.Background(), p)
		require.NoError(t, e)
		require.NotEmpty(t, edits, text)
		updated := text
		for i := len(edits) - 1; i >= 0; i-- {
			ed := edits[i]
			updated = updated[:positionByte(updated, ed.Range.Start)] + ed.NewText + updated[positionByte(updated, ed.Range.End):]
		}
		var host yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte(updated), &host), updated)
		if strings.Contains(text, "next") {
			require.Contains(t, updated, "next: preserved")
		}
		openFeature(t, h, uri, updated)
		edits, e = h.Formatting(context.Background(), p)
		require.NoError(t, e)
		require.Empty(t, edits, updated)
	}
}

func TestFormatMethodChainLambda(t *testing.T) {
	h, uri := featureHandler(t)
	text := `#!sample_from custom_sample.json

map embed_input {
  let movie = this
  let segments = [
    ["Title", $movie.title],
    ["Overview", $movie.overview],
    ["Tagline", $movie.tagline],
    [
      "Cast",
      $movie.credits.cast.
        slice(0, 10).
        # character can be null
        map_each(c -> c.with("character", "name").values().filter(cn -> cn.or("") != "").join(" – ")).
        join("; "),
    ],
  ]
  root = $segments
}
root = this.apply("embed_input")
`
	formatted, ok := h.formatText(uri, text, 2)
	require.True(t, ok)
	t.Log(formatted)
	require.Contains(t, formatted, `join(" – ")`)
	require.Contains(t, formatted, `      $movie.credits.cast.
        slice(0, 10).
        # character can be null
        map_each(c -> c.
          with("character", "name").
          values().
          filter(cn -> cn.or("") != "").
          join(" – ")
        ).
        join("; "),`)
	require.Contains(t, formatted, "#!sample_from custom_sample.json\n\nmap")
	twice, ok := h.formatText(uri, formatted, 2)
	require.True(t, ok)
	require.Equal(t, formatted, twice)
}

func TestFormattingCallCommentsAndChainWidths(t *testing.T) {
	h, uri := featureHandler(t)
	for _, width := range []int{40, 80, 120} {
		require.NoError(t, os.WriteFile(filepath.Join(h.workspaceRoot, ".bloblangrc.json"), []byte(fmt.Sprintf(`{"formatter":{"printWidth":%d}}`, width)), 0600))
		for _, text := range []string{
			`root = this.foo.uppercase().trim().replace("old", "new").split(";").join(" | ")`,
			"root = this.foo.replace(\n# first argument\n\"a\", # replacement\n\"b\"\n)\n",
			"root = this.foo.replace(\"a\", \"b\" # final argument\n)\n",
			"root = this.foo.\n# first\nuppercase().\n# second\ntrim()\n",
			`root = this.items.map_each(item -> item.with("title", "name").values().join(" – ")).join("; ")`,
		} {
			formatted, ok := h.formatText(uri, text, 2)
			require.True(t, ok, text)
			twice, ok := h.formatText(uri, formatted, 2)
			require.True(t, ok)
			require.Equal(t, formatted, twice)
			_, err := h.benv.Parse(formatted)
			require.NoError(t, err, formatted)
		}
	}
}
