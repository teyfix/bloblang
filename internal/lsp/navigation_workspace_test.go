package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/stretchr/testify/require"
)

func TestReferencesAllRootsYAMLAndOpenBuffers(t *testing.T) {
	h, uri := featureHandler(t)
	source := "map transform { root = this }\nroot = this.apply(\"transform\")\n"
	openFeature(t, h, uri, source)
	secondRoot := t.TempDir()
	h.workspaceRoots = []string{h.workspaceRoot, secondRoot}
	mappingPath, err := uriToPath(uri)
	require.NoError(t, err)
	yamlURI := fileURI(filepath.Join(secondRoot, "pipeline.yaml"))
	yamlText := "mapping: |\n  import \"" + mappingPath + "\"\n  root = this.apply(\"transform\")\n"
	yamlPath, _ := uriToPath(yamlURI)
	require.NoError(t, os.WriteFile(yamlPath, []byte(yamlText), 0600))
	unsavedURI := fileURI(filepath.Join(secondRoot, "unsaved.blobl"))
	openFeature(t, h, unsavedURI, "import \""+mappingPath+"\"\nroot = this.apply(\"transform\")")
	unrelatedURI := fileURI(filepath.Join(secondRoot, "unrelated.blobl"))
	openFeature(t, h, unrelatedURI, "map transform { root = this }\nroot = this.apply(\"transform\")")
	for _, generated := range []string{".cache", ".state"} {
		require.NoError(t, os.MkdirAll(filepath.Join(secondRoot, generated), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(secondRoot, generated, "ignored.yaml"), []byte(yamlText), 0600))
	}
	before := h.documents.URIs()
	params := &protocol.ReferenceParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Position: bytePosition(source, strings.Index(source, "transform"))}}
	for _, include := range []bool{false, true} {
		params.Context.IncludeDeclaration = include
		refs, err := h.References(context.Background(), params)
		require.NoError(t, err)
		want := 3
		if include {
			want++
		}
		require.Len(t, refs, want)
		gotURIs := map[protocol.DocumentURI]int{}
		for _, ref := range refs {
			gotURIs[ref.URI]++
		}
		require.Equal(t, 1, gotURIs[yamlURI])
		require.Equal(t, 1, gotURIs[unsavedURI])
		require.Zero(t, gotURIs[unrelatedURI])
	}
	require.ElementsMatch(t, before, h.documents.URIs())
}

func TestReferencesFileImportsInsideYAMLAcrossRoots(t *testing.T) {
	h, uri := featureHandler(t)
	mappingPath, err := uriToPath(uri)
	require.NoError(t, err)
	second := t.TempDir()
	h.workspaceRoots = []string{h.workspaceRoot, second}
	text := "import \"" + mappingPath + "\"\nroot = this\n"
	openFeature(t, h, uri, text)
	yamlURI := fileURI(filepath.Join(second, "pipeline.yaml"))
	yamlText := "mapping: |\n  import \"" + mappingPath + "\"\n  root = this\n"
	path, _ := uriToPath(yamlURI)
	require.NoError(t, os.WriteFile(path, []byte(yamlText), 0600))
	refs, err := h.References(context.Background(), &protocol.ReferenceParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Position: bytePosition(text, strings.Index(text, mappingPath))}})
	require.NoError(t, err)
	require.Len(t, refs, 2)
	found := false
	for _, ref := range refs {
		if ref.URI == yamlURI {
			found = true
			require.Equal(t, 1, ref.Range.Start.Line)
			require.Equal(t, "\""+mappingPath+"\"", yamlText[positionByte(yamlText, ref.Range.Start):positionByte(yamlText, ref.Range.End)])
		}
	}
	require.True(t, found)
}

func TestReferencesRepeatedLetBindingsAndCancellation(t *testing.T) {
	h, uri := featureHandler(t)
	text := "let a = 1\nif true { let a = 2 }\nroot = $a\n"
	openFeature(t, h, uri, text)
	params := &protocol.ReferenceParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Position: bytePosition(text, strings.Index(text, "a ="))}, Context: protocol.ReferenceContext{IncludeDeclaration: true}}
	refs, err := h.References(context.Background(), params)
	require.NoError(t, err)
	require.Len(t, refs, 3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = h.References(ctx, params)
	require.ErrorIs(t, err, context.Canceled)
}
