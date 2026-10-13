package lsp

import (
	"context"
	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspacePreviewReloadAndDeclarationHover(t *testing.T) {
	h, uri := featureHandler(t)
	lang, body := h.preview(uri, `{"name":"Ada","items":[1,2]}`)
	require.Equal(t, "yaml", lang)
	require.Contains(t, body, "name: Ada")
	require.NoError(t, os.WriteFile(filepath.Join(h.workspaceRoot, ".bloblangrc.json"), []byte(`{"formatter":{"printWidth":30},"preview":{"format":"json"}}`), 0600))
	lang, body = h.preview(uri, `{"a":1}`)
	require.Equal(t, "json", lang)
	require.Equal(t, `{"a": 1}`, body)
	text := "#!input {\"name\":\"Ada\"}\nlet person = this.name\nroot = $person\n"
	openFeature(t, h, uri, text)
	v := hoverAt(t, h, uri, text, "person =")
	require.NotNil(t, v)
	require.Contains(t, v.Contents.Value(), "Ada")
	v = hoverAt(t, h, uri, text, "$person")
	require.NotNil(t, v)
	require.Contains(t, v.Contents.Value(), "Ada")
	require.NoError(t, os.WriteFile(filepath.Join(h.workspaceRoot, ".bloblangrc.json"), []byte(`{"preview":{"format":"bogus"}}`), 0600))
	lang, _ = h.preview(uri, `{"a":1}`)
	require.Equal(t, "yaml", lang)
}
func TestDeletedAndExpressionOnlyDocumentation(t *testing.T) {
	h, uri := featureHandler(t)
	text := "root.foo = deleted()"
	openFeature(t, h, uri, text)
	v := hoverAt(t, h, uri, text, "deleted")
	require.NotNil(t, v)
	require.Contains(t, v.Contents.Value(), "deleted")
	uri = fileURI(filepath.Join(h.workspaceRoot, "config.yaml"))
	text = "check: |\n  !errored() && this.state != \"processing-ready\"\n"
	openFeature(t, h, uri, text)
	v = hoverAt(t, h, uri, text, "errored")
	require.NotNil(t, v)
	require.Contains(t, v.Contents.Value(), "errored")
}
func TestInputDirectiveCompletedClearsErrors(t *testing.T) {
	h, uri := featureHandler(t)
	for _, text := range []string{"#!input {\"foo\":\nroot = this", "#!input {\"foo\":\"bar\"}\nroot = this"} {
		openFeature(t, h, uri, text)
		ds := h.diagnosticsText(uri, text)
		if strings.Contains(text, "bar") {
			for _, d := range ds {
				require.NotContains(t, d.Message, "unexpected")
				require.NotContains(t, d.Message, "end")
			}
			require.NotNil(t, h.getSample(uri))
		} else {
			require.NotEmpty(t, ds)
		}
	}
}

func TestDirectiveWithoutMappingIsValidEditorState(t *testing.T) {
	h, uri := featureHandler(t)
	text := `#!input {"foo":"bar"}`
	openFeature(t, h, uri, text)
	require.Empty(t, h.diagnosticsText(uri, text))
	require.NotNil(t, h.getSample(uri))
}

func TestMissingEnvConcatenationValidationAndHover(t *testing.T) {
	name := "BLOBLANG_LSP_TEST_HOVER_MISSING_ENV"
	value, exists := os.LookupEnv(name)
	require.NoError(t, os.Unsetenv(name))
	t.Cleanup(func() {
		if exists {
			_ = os.Setenv(name, value)
		} else {
			_ = os.Unsetenv(name)
		}
	})
	h, uri := featureHandler(t)
	text := `root = ["email=" + env("` + name + `"), "ref=" + this.provider_file_ref]`
	openFeature(t, h, uri, text)
	ds := h.diagnosticsText(uri, text)
	require.NotEmpty(t, ds)
	formatted, valid := h.formatText(uri, text, 2)
	require.True(t, valid)
	require.Contains(t, formatted, "email=")
	twice, valid := h.formatText(uri, formatted, 2)
	require.True(t, valid)
	require.Equal(t, formatted, twice)
	require.Positive(t, lintCodes(h, uri, text)["correctness/environment/require-fallback"])
	for _, d := range ds {
		require.NotEqual(t, protocol.SeverityError, *d.Severity, d.Message)
		require.NotContains(t, d.Message, "cannot add types")
	}
	text = "#!input {\"provider_file_ref\":\"fixture-ref\"}\n" + text
	openFeature(t, h, uri, text)
	v := hoverAt(t, h, uri, text, "env(")
	require.NotNil(t, v)
	require.Contains(t, v.Contents.Value(), "env")
	v = hoverAt(t, h, uri, text, "\"email=\" +")
	require.NotNil(t, v)
	v = hoverAt(t, h, uri, text, "+ env(")
	require.NotNil(t, v)
	require.Contains(t, v.Contents.Value(), "email=")
	// Selecting the closing argument evaluates env() itself and displays the placeholder.
	v = hoverAt(t, h, uri, text, `), "ref="`)
	require.NotNil(t, v)
	require.Contains(t, v.Contents.Value(), `""`)
	hints, err := h.InlayHint(context.Background(), &protocol.InlayHintParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Range: protocol.Range{End: protocol.Position{Line: 10000}}})
	require.NoError(t, err)
	require.NotEmpty(t, hints)
	require.Contains(t, string(hints[len(hints)-1].Label), "email=")
	require.Contains(t, string(hints[len(hints)-1].Label), "fixture-ref")
	require.Positive(t, lintCodes(h, uri, text)["correctness/environment/require-fallback"])
	text = `#!sample {"input":{"provider_file_ref":"fixture-ref"},"env":{"` + name + `":"fixture-email"}}` + "\n" + strings.SplitN(text, "\n", 2)[1]
	openFeature(t, h, uri, text)
	v = hoverAt(t, h, uri, text, "+ env(")
	require.NotNil(t, v)
	require.Contains(t, v.Contents.Value(), "email=fixture-email")
	require.Positive(t, lintCodes(h, uri, text)["correctness/environment/require-fallback"])
}
