package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestFormattingPreservesYAMLPropertiesAndTrivia(t *testing.T) {
	tooling, dir := testTooling(t)
	for _, tc := range []struct{ name, input, want string }{
		{"block comment", "mapping: | # preserve header\n  root =   1\nnext: preserved\n", "mapping: | # preserve header\n  root = 1\nnext: preserved\n"},
		{"anchor and alias", "mapping: &code |\n  root =   1\n\ncopy: *code # preserve alias\n", "mapping: &code |\n  root = 1\n\ncopy: *code # preserve alias\n"},
		{"tag and anchor", "mapping: !!str &code |2- # preserve header\n  root =   1\n\nnext: preserved\n", "mapping: !!str &code |2- # preserve header\n  root = 1\n\nnext: preserved\n"},
		{"multiline properties", "mapping: &code # anchor comment\n  !!str | # scalar comment\n  root =   1\nnext: preserved\n", "mapping: &code # anchor comment\n  !!str | # scalar comment\n  root = 1\nnext: preserved\n"},
		{"quoted anchor", "mapping: &code 'root =   1' # preserve comment\ncopy: *code\n", "mapping: &code 'root = 1' # preserve comment\ncopy: *code\n"},
		{"quoted tag", "mapping: !!str \"root =   1\"\nnext: preserved\n", "mapping: !!str \"root = 1\"\nnext: preserved\n"},
		{"folded keep", "mapping: >+ # preserve header\n  root = {foo: 1,\n  bar: 2}\n\nnext: preserved\n", "mapping: |+ # preserve header\n  root = { foo: 1, bar: 2 }\n\nnext: preserved\n"},
		{"trailing blank spaces", "mapping: |+ # preserve header\n  root =   1\n    \n\nnext: preserved\n", "mapping: |+ # preserve header\n  root = 1\n    \n\nnext: preserved\n"},
		{"no final newline", "mapping: |- # preserve header\n  root =   1", "mapping: |- # preserve header\n  root = 1"},
		{"CRLF", "mapping: |- # preserve header\r\n  root =   1\r\n\r\nnext: preserved\r\n", "mapping: |- # preserve header\r\n  root = 1\r\n\r\nnext: preserved\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "mapping.yaml")
			got, err := tooling.Format(path, tc.input, 2)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			again, err := tooling.Format(path, got, 2)
			require.NoError(t, err)
			require.Equal(t, got, again)
		})
	}
}

func TestWrappedScalarPreservesInlineHostComment(t *testing.T) {
	tooling, dir := testTooling(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".bloblangrc.json"), []byte(`{"formatter":{"printWidth":20}}`), 0600))
	path := filepath.Join(dir, "mapping.yaml")
	input := "mapping: &code 'root = {foo: this.foo, bar: this.bar}' # host comment\ncopy: *code\n"
	got, err := tooling.Format(path, input, 2)
	require.NoError(t, err)
	require.NotEqual(t, input, got)
	require.Contains(t, got, " # host comment\ncopy: *code\n")
	regions := extractRegions(fileURI(path), got)
	require.Len(t, regions, 1)
	require.NotContains(t, regions[0].text, "# host comment")
	require.Contains(t, regions[0].text, "\n")
	again, err := tooling.Format(path, got, 2)
	require.NoError(t, err)
	require.Equal(t, got, again)
}

func TestLintQuickFixPreservesMultilineYAML(t *testing.T) {
	for _, header := range []string{"|-", "| # header", "&code |2- # header"} {
		for _, eol := range []string{"\n", "\r\n"} {
			t.Run(header+eol, func(t *testing.T) {
				h, _ := featureHandler(t)
				uri := fileURI(filepath.Join(h.workspaceRoot, "mapping.yaml"))
				input := "mapping: " + header + "\n  root = {\n    a: 1,\n    b: 2\n  }.without(\"a\").without(\"b\")\n\nnext: preserved # host\n"
				input = strings.ReplaceAll(input, "\n", eol)
				openFeature(t, h, uri, input)
				actions, err := h.CodeAction(context.Background(), &protocol.CodeActionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Range: protocol.Range{End: protocol.Position{Line: 99}}})
				require.NoError(t, err)
				require.Len(t, actions, 1)
				got := applyToolEdits(input, actions[0].Edit.Changes[uri])
				require.Contains(t, got, "  }.without(\"a\", \"b\")")
				require.Contains(t, got, "mapping: "+header+eol)
				require.Contains(t, got, eol+eol+"next: preserved # host"+eol)
				var host yaml.Node
				require.NoError(t, yaml.Unmarshal([]byte(got), &host))
				regions := extractRegions(uri, got)
				require.Len(t, regions, 1)
				_, err = h.benv.Parse(regions[0].text)
				require.NoError(t, err)
				require.Zero(t, lintCodes(h, regions[0].uri, regions[0].text)["style/objects/combine-without"])
				tooling, err := NewTooling()
				require.NoError(t, err)
				defer tooling.Close()
				fixed, err := tooling.Lint(filepath.Join(h.workspaceRoot, "mapping.yaml"), input, true)
				require.NoError(t, err)
				require.Equal(t, got, fixed.Text)
			})
		}
	}
}

func TestEmbeddedEditRejectsHostMutation(t *testing.T) {
	text := "mapping: |- # host comment\n  root = this\nnext: preserved\n"
	r := extractRegions("file:///mapping.yaml", text)[0]
	require.False(t, r.validReplacement(text, "|-\n  root = this\n", r.text), "dropping a host comment must fail validation")
	require.False(t, r.validReplacement(text, "|- # host comment\n  root = this\nother: added\n", r.text), "adding a host key must fail validation")
}

func TestFormattingCommentAttachmentAndSuppression(t *testing.T) {
	h, uri := featureHandler(t)
	for _, input := range []string{
		"root = 1 # first assignment\nroot = 2\n",
		"root = [1, # first item\n2]\n",
		"root = {first: 1, # first field\nsecond: 2}\n",
	} {
		formatted, ok := h.formatText(uri, input, 2)
		require.True(t, ok)
		for _, line := range strings.Split(input, "\n") {
			if i := strings.Index(line, " #"); i >= 0 {
				require.Contains(t, formatted, line[i-2:]+"\n")
			}
		}
		again, ok := h.formatText(uri, formatted, 2)
		require.True(t, ok)
		require.Equal(t, formatted, again)
	}
	const rule = "correctness/environment/require-fallback"
	for _, input := range []string{
		"# bloblang-lint-disable-next-line " + rule + "\nroot = {first: env(\"X\"),second: this.foo,third: this.bar,fourth: this.baz,fifth: this.qux}\nroot.other = env(\"Y\")\n",
		"root = {\n# bloblang-lint-disable-next-line " + rule + "\nfirst: env(\"X\").or(env(\"OTHER\")),\nsecond: env(\"Y\")\n}\n",
	} {
		require.Equal(t, 1, lintCodes(h, uri, input)[rule])
		formatted, ok := h.formatText(uri, input, 2)
		require.True(t, ok)
		require.Equal(t, 1, lintCodes(h, uri, formatted)[rule], formatted)
	}
}

func TestFormattingPreservesCRLF(t *testing.T) {
	h, uri := featureHandler(t)
	input := "root = {a: 1, # first\r\nb: 2}\r\nroot.x =  3\r\n"
	got, ok := h.formatText(uri, input, 2)
	require.True(t, ok)
	require.Equal(t, "root = {\r\n  a: 1, # first\r\n  b: 2\r\n}\r\nroot.x = 3\r\n", got)
	again, ok := h.formatText(uri, got, 2)
	require.True(t, ok)
	require.Equal(t, got, again)
	got, ok = h.formatText(uri, "root =   1\r\n# final comment", 2)
	require.True(t, ok)
	require.Equal(t, "root = 1\r\n# final comment\r\n", got)
	chain := "root = this.\r\n# chain comment\r\nuppercase().trim()\r\n"
	got, ok = h.formatText(uri, chain, 2)
	require.True(t, ok)
	require.NotContains(t, got, "\r\r")
	again, ok = h.formatText(uri, got, 2)
	require.True(t, ok)
	require.Equal(t, got, again)
}

func FuzzEmbeddedFormatPreservesValuesAndHost(f *testing.F) {
	for _, value := range []string{"", "ordinary", "quotes ' and \"", "new\nline\r\ntab\t", "Unicode 😀 λ", "# comment: &anchor"} {
		for _, style := range []uint8{0, 1, 2, 3} {
			f.Add(value, style)
		}
	}
	tooling, err := NewTooling()
	require.NoError(f, err)
	f.Cleanup(tooling.Close)
	path := filepath.Join(f.TempDir(), "mapping.yaml")
	f.Fuzz(func(t *testing.T, value string, style uint8) {
		if len(value) > 2048 {
			t.Skip()
		}
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		source := "root =   {\"value\":" + string(encoded) + "}"
		var scalar string
		switch style % 4 {
		case 0:
			scalar = "&code |- # host comment\n  " + source
		case 1:
			scalar = "!!str &code |+ # host comment\n  " + source + "\n"
		case 2:
			scalar = "&code '" + strings.ReplaceAll(source, "'", "''") + "' # host comment"
		case 3:
			quoted, err := json.Marshal(source)
			require.NoError(t, err)
			scalar = "&code !!str " + string(quoted) + " # host comment"
		}
		input := "before: {value: untouched}\nmapping: " + scalar + "\ncopy: *code\nafter: untouched\n"
		if style&4 != 0 {
			input = strings.ReplaceAll(input, "\n", "\r\n")
		}
		var originalHost map[string]any
		if yaml.Unmarshal([]byte(input), &originalHost) != nil {
			t.Skip("generated string contains characters forbidden by YAML")
		}
		original, err := tooling.h.benv.Parse(originalHost["mapping"].(string))
		if err != nil {
			t.Skip("string cannot be represented by the Bloblang parser")
		}
		want, err := original.Query(nil)
		require.NoError(t, err)
		formatted, err := tooling.Format(path, input, 2)
		require.NoError(t, err)
		require.Contains(t, formatted, "# host comment")
		var host map[string]any
		require.NoError(t, yaml.Unmarshal([]byte(formatted), &host))
		require.Equal(t, map[string]any{"value": "untouched"}, host["before"])
		require.Equal(t, "untouched", host["after"])
		require.Equal(t, host["mapping"], host["copy"])
		mapped, ok := host["mapping"].(string)
		require.True(t, ok)
		executor, err := tooling.h.benv.Parse(mapped)
		require.NoError(t, err)
		got, err := executor.Query(nil)
		require.NoError(t, err)
		require.Equal(t, want, got)
		again, err := tooling.Format(path, formatted, 2)
		require.NoError(t, err)
		require.Equal(t, formatted, again)
	})
}
