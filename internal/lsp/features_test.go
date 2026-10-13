package lsp

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/stretchr/testify/require"
	"github.com/teyfix/bloblang-lsp/internal/config"
)

func featureHandler(t *testing.T) (*Handler, protocol.DocumentURI) {
	t.Helper()
	cfg, err := config.Load()
	require.NoError(t, err)
	h, err := NewHandler(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	dir := t.TempDir()
	h.workspaceRoot = dir
	return h, fileURI(filepath.Join(dir, "mapping.blobl"))
}
func openFeature(t *testing.T, h *Handler, uri protocol.DocumentURI, text string) {
	t.Helper()
	require.NoError(t, h.DidOpen(context.Background(), &protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "bloblang", Text: text}}))
}
func hoverAt(t *testing.T, h *Handler, uri protocol.DocumentURI, text, needle string) *protocol.Hover {
	t.Helper()
	idx := strings.Index(text, needle)
	require.GreaterOrEqual(t, idx, 0)
	p := &protocol.HoverParams{}
	p.TextDocument.URI = uri
	p.Position = bytePosition(text, idx)
	v, err := h.Hover(context.Background(), p)
	require.NoError(t, err)
	return v
}
func TestSampledExpressionHoverAndMetadata(t *testing.T) {
	h, uri := featureHandler(t)
	text := "#!input {\"a\":1,\"s\":\"hey\",\"root\":\"property\"}\n#!meta {\"k\":\"v\"}\nlet root = \"variable\"\nroot.a = 9\nroot.b = this.a\nroot.c = root.a\nroot.m = meta(\"k\")\nmeta k = \"changed\"\nroot.n = @k\nroot.z = this.s.uppercase()\nroot.prop = this.root\nroot.var = $root\nroot.bad = ("
	openFeature(t, h, uri, text)
	for needle, want := range map[string]string{"this.a": "1", "root.a\nroot.m": "9", "this.s": "hey", "this.root": "property", "$root": "variable", "@k": "changed"} {
		v := hoverAt(t, h, uri, text, needle)
		require.NotNil(t, v, needle)
		require.Contains(t, v.Contents.Value(), want, needle)
	}
	hints, err := h.InlayHint(context.Background(), &protocol.InlayHintParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Range: protocol.Range{End: protocol.Position{Line: 10000}}})
	require.NoError(t, err)
	var values []string
	for _, v := range hints {
		values = append(values, string(v.Label))
	}
	require.Contains(t, strings.Join(values, "\n"), "HEY")
}
func TestFirstExpressionUnicodeAndScopedHover(t *testing.T) {
	h, uri := featureHandler(t)
	text := "#!input {\"s\":\"world\",\"list\":[{\"s\":\"inner\"}]}\nroot.x = \"😀\" + this.s\nroot.items = this.list.map_each(this.s)"
	openFeature(t, h, uri, text)
	v := hoverAt(t, h, uri, text, "this.s\n")
	require.NotNil(t, v)
	require.Contains(t, v.Contents.Value(), "world")
	require.Equal(t, 16, v.Range.Start.Character)
	p := &protocol.HoverParams{}
	p.TextDocument.URI = uri
	p.Position = bytePosition(text, strings.LastIndex(text, "this.s"))
	v, err := h.Hover(context.Background(), p)
	require.NoError(t, err)
	require.Nil(t, v)
}
func TestYAMLRegionsAndFormatting(t *testing.T) {
	h, uri := featureHandler(t)
	uri = protocol.DocumentURI(strings.TrimSuffix(string(uri), ".blobl") + ".yaml")
	text := "processors:\n  - mapping: |\n      #!input {\"name\":\"Ada\"}\n      root.name=this.name.uppercase()\n  - log:\n      message: '${! this.name.uppercase() }'\nother: unrelated\n"
	openFeature(t, h, uri, text)
	regions := h.regions(uri)
	require.Len(t, regions, 2)
	v := hoverAt(t, h, uri, text, "this.name.uppercase()")
	require.NotNil(t, v)
	require.Contains(t, v.Contents.Value(), "Ada")
	require.Equal(t, 3, v.Range.Start.Line)
	p := &protocol.DocumentFormattingParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Options: protocol.FormattingOptions{TabSize: 2}}
	edits, err := h.Formatting(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, edits, 1)
	e := edits[0]
	updated := text[:positionByte(text, e.Range.Start)] + e.NewText + text[positionByte(text, e.Range.End):]
	require.Contains(t, updated, "root.name = this.name.uppercase()")
	require.Contains(t, updated, "other: unrelated")
	openFeature(t, h, uri, updated)
	edits, err = h.Formatting(context.Background(), p)
	require.NoError(t, err)
	require.Empty(t, edits)
}
func TestMapNavigationAndForwardDefinition(t *testing.T) {
	h, uri := featureHandler(t)
	text := "#!input \"Ada\"\nroot = this.apply(\"person-name\")\nmap \"person-name\" { root = this.uppercase() }"
	openFeature(t, h, uri, text)
	p := &protocol.DefinitionParams{}
	p.TextDocument.URI = uri
	p.Position = bytePosition(text, strings.Index(text, "person-name"))
	locations, err := h.Definition(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, locations, 1)
	require.Equal(t, 2, locations[0].Range.Start.Line)
	hints, err := h.InlayHint(context.Background(), &protocol.InlayHintParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Range: protocol.Range{End: protocol.Position{Line: 10000}}})
	require.NoError(t, err)
	require.NotEmpty(t, hints)
	require.Contains(t, string(hints[len(hints)-1].Label), "ADA")
}
func TestDefaultSamplesAndNonblockingFailures(t *testing.T) {
	h, uri := featureHandler(t)
	path, _ := uriToPath(uri)
	sample := strings.TrimSuffix(path, ".blobl") + ".sample.yaml"
	require.NoError(t, os.WriteFile(sample, []byte("$bloblang:\n  input: null\n  meta:\n    k: value\n"), 0600))
	text := "root = meta(\"k\")"
	openFeature(t, h, uri, text)
	require.NotNil(t, h.getSample(uri))
	require.Nil(t, h.getSample(uri).Value)
	text = "#!input_from absent.yaml\nroot = this.name.uppercase()"
	openFeature(t, h, uri, text)
	require.Nil(t, h.getSample(uri))
	ds := h.diagnosticsText(uri, text)
	require.NotEmpty(t, ds)
	require.Equal(t, protocol.SeverityError, *ds[0].Severity)
	require.Equal(t, 0, ds[0].Range.Start.Line)
	v := hoverAt(t, h, uri, text, "uppercase")
	require.NotNil(t, v)
	require.Contains(t, v.Contents.Value(), "uppercase")
}

func TestConditionalReachedBranchHover(t *testing.T) {
	h, uri := featureHandler(t)
	text := "#!input {\"pick\":true,\"name\":\"Ada\"}\nroot.before = 1\nif this.pick {\n  let x = this.name\n  root.name = $x.uppercase()\n  root.copy = root.name\n} else {\n  root.other = this.name\n}"
	openFeature(t, h, uri, text)
	v := hoverAt(t, h, uri, text, "$x.uppercase()")
	require.NotNil(t, v)
	require.Contains(t, v.Contents.Value(), "Ada")
	v = hoverAt(t, h, uri, text, "root.name\n}")
	require.NotNil(t, v)
	require.Contains(t, v.Contents.Value(), "ADA")
	v = hoverAt(t, h, uri, text, "this.name\n}")
	require.Nil(t, v, "unexecuted branch must not show an invented value")
}
func TestCompletionTypeGuidanceAndStaticFallback(t *testing.T) {
	h, uri := featureHandler(t)
	text := "#!input {\"name\":\"Ada\"}\nroot = this.name."
	openFeature(t, h, uri, text)
	p := &protocol.CompletionParams{}
	p.TextDocument.URI = uri
	p.Position = bytePosition(text, len(text))
	list, err := h.Completion(context.Background(), p)
	require.NoError(t, err)
	labels := []string{}
	for _, i := range list.Items {
		labels = append(labels, i.Label)
	}
	joined := strings.Join(labels, " ")
	require.Contains(t, joined, "uppercase")
	require.Contains(t, joined, "catch")
	require.NotContains(t, labels, "round")
	text = "root = this.name."
	openFeature(t, h, uri, text)
	p.Position = bytePosition(text, len(text))
	list, err = h.Completion(context.Background(), p)
	require.NoError(t, err)
	labels = nil
	for _, i := range list.Items {
		labels = append(labels, i.Label)
	}
	require.Contains(t, strings.Join(labels, " "), "round")
}
func TestMultilineQuotedYAMLAndExternalMappingPaths(t *testing.T) {
	h, uri := featureHandler(t)
	uri = protocol.DocumentURI(strings.TrimSuffix(string(uri), ".blobl") + ".yaml")
	text := "mapping: 'root = this.name.\n  uppercase()'\nfields_mapping: \"root = this.name.\\nuppercase()\"\nquoted: \"emoji 😀 ${! this.name.uppercase() }\"\n"
	openFeature(t, h, uri, text)
	require.Len(t, h.regions(uri), 3)
	v := hoverAt(t, h, uri, text, "uppercase()")
	require.NotNil(t, v)
	require.Contains(t, v.Contents.Value(), "uppercase")
	require.Equal(t, 1, v.Range.Start.Line)
	require.NoError(t, os.MkdirAll(filepath.Join(h.workspaceRoot, "svc"), 0755))
	path := filepath.Join(h.workspaceRoot, "svc", "mapping.blobl")
	require.NoError(t, os.WriteFile(path, []byte("root = this"), 0600))
	uri = fileURI(filepath.Join(h.workspaceRoot, "svc", "config.yaml"))
	text = "mapping: from \"svc/mapping.blobl\"\n"
	openFeature(t, h, uri, text)
	require.Empty(t, h.regions(uri))
	p := &protocol.DefinitionParams{}
	p.TextDocument.URI = uri
	p.Position = bytePosition(text, strings.Index(text, "svc/mapping"))
	locations, err := h.Definition(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, locations, 1)
	require.Equal(t, fileURI(path), locations[0].URI)
	require.Empty(t, h.yamlPathDiagnostics(uri, text))
}
func TestVariableScopeAndReferencesDoNotOpenFiles(t *testing.T) {
	h, uri := featureHandler(t)
	text := "let x = 1\nif true { let x = 2 }\nroot = $x\nmap person { root = $x }"
	openFeature(t, h, uri, text)
	p := &protocol.DefinitionParams{}
	p.TextDocument.URI = uri
	p.Position = bytePosition(text, strings.Index(text, "$x"))
	locations, err := h.Definition(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, locations, 1)
	require.Equal(t, 1, locations[0].Range.Start.Line)
	p.Position = bytePosition(text, strings.LastIndex(text, "$x"))
	locations, err = h.Definition(context.Background(), p)
	require.NoError(t, err)
	require.Empty(t, locations)
	path, _ := uriToPath(uri)
	other := filepath.Join(filepath.Dir(path), "other.blobl")
	require.NoError(t, os.WriteFile(other, []byte("import \"mapping.blobl\"\nroot = this.apply(\"person\")"), 0600))
	require.NoError(t, os.WriteFile(path, []byte(text), 0600))
	before := h.documents.URIs()
	rp := &protocol.ReferenceParams{}
	rp.TextDocument.URI = uri
	rp.Position = bytePosition(text, strings.Index(text, "person"))
	_, err = h.References(context.Background(), rp)
	require.NoError(t, err)
	require.Equal(t, before, h.documents.URIs())
}
func TestFormatterPreservesTokensAndIdempotence(t *testing.T) {
	h, uri := featureHandler(t)
	text := "#!input {\"name\": \"Ada\"}\n# comment  kept\nroot.message=  \"a  b😀\".uppercase( )\nroot.name=this.name\n"
	openFeature(t, h, uri, text)
	formatted, ok := h.formatText(uri, text, 2)
	require.True(t, ok)
	require.Contains(t, formatted, "\"a  b😀\"")
	require.Contains(t, formatted, "# comment  kept")
	twice, ok := h.formatText(uri, formatted, 2)
	require.True(t, ok)
	require.Equal(t, formatted, twice)
	tree, err := h.parser.Parse("before", text)
	require.NoError(t, err)
	defer tree.Close()
	var before []formatToken
	tokens(tree.RootNode(), []byte(text), &before)
	afterTree, err := h.parser.Parse("after", formatted)
	require.NoError(t, err)
	defer afterTree.Close()
	var after []formatToken
	tokens(afterTree.RootNode(), []byte(formatted), &after)
	require.Len(t, after, len(before))
	for i := range before {
		require.Equal(t, before[i].text, after[i].text)
	}
	malformed := "root = ("
	unchanged, ok := h.formatText(uri, malformed, 2)
	require.False(t, ok)
	require.Equal(t, malformed, unchanged)
}

func TestYAMLBlockStopsBeforeSequenceItemSibling(t *testing.T) {
	h, uri := featureHandler(t)
	uri = protocol.DocumentURI(strings.TrimSuffix(string(uri), ".blobl") + ".yaml")
	text := "processors:\n  - mapping: |\n      root = this.name\n    label: stage\n"
	openFeature(t, h, uri, text)
	regions := h.regions(uri)
	require.Len(t, regions, 1)
	require.Equal(t, "root = this.name\n", regions[0].text)
	formatted, ok := h.formatText(regions[0].uri, regions[0].text, 2)
	require.True(t, ok)
	require.Equal(t, regions[0].text, formatted)
}
