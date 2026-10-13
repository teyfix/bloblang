package lsp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/owenrumney/go-lsp/servertest"
	"github.com/stretchr/testify/require"
)

func TestSignatureHelpWhileTyping(t *testing.T) {
	for _, tc := range []struct {
		name, source, signature string
		parameter               int
	}{
		{"incomplete function", `root = range(`, "range(", 0},
		{"incomplete method", `root = this.slice(0, `, "slice(", 1},
		{"nested array and call", `root = range(0, [1, 2].length(), `, "range(", 2},
		{"inside array argument", `root = this.append([1, 2`, "append(", 0},
		{"innermost call", `root = range(0, range(1, `, "range(", 1},
		{"named reordered", `root = range(stop: 10, start: `, "range(", 0},
		{"named second", `root = range(stop: `, "range(", 1},
		{"quoted comma", `root = "x".replace("a,b", `, "replace(", 1},
		{"escaped quote", `root = "x".replace("a\",b", `, "replace(", 1},
		{"triple quoted comma", `root = "x".replace("""a,"b""", `, "replace(", 1},
		{"comment comma", "root = range(0, # , range(\n", "range(", 1},
		{"variadic", `root = this.with("a", "b", `, "with(...args)", 0},
		{"unicode", `root = "😀".replace("a", `, "replace(", 1},
		{"zero args", `root = uuid_v4(`, "uuid_v4()", -1},
		{"completed call", `root = range(0, 10)`, "", -1},
		{"unknown call", `root = unknown(`, "", -1},
		{"inside comment", `root = range(0, # range(`, "", -1},
		{"fake string call", `root = "range(`, "", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, uri := featureHandler(t)
			openFeature(t, h, uri, tc.source)
			got, err := h.SignatureHelp(context.Background(), &protocol.SignatureHelpParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Position: bytePosition(tc.source, len(tc.source))}})
			require.NoError(t, err)
			if tc.signature == "" {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Len(t, got.Signatures, 1)
			require.Contains(t, got.Signatures[0].Label, tc.signature)
			if tc.parameter < 0 {
				require.Nil(t, got.ActiveParameter)
			} else {
				require.Equal(t, tc.parameter, *got.ActiveParameter)
			}
		})
	}
}

func TestSignatureHelpYAMLAndParameterDocumentation(t *testing.T) {
	h, _ := featureHandler(t)
	uri := fileURI(filepath.Join(h.workspaceRoot, "pipeline.yaml"))
	source := "mapping: |\n  root = range(0, 10,\n"
	openFeature(t, h, uri, source)
	got, err := h.SignatureHelp(context.Background(), &protocol.SignatureHelpParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Position: bytePosition(source, strings.Index(source, "10,")+3)}})
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, 2, *got.ActiveParameter)
	require.Contains(t, got.Signatures[0].Label, "step: integer = 1")
	require.NotEmpty(t, got.Signatures[0].Parameters[0].Documentation.Value)
}

func TestDocumentSymbolsHierarchyAndUTF16(t *testing.T) {
	h, uri := featureHandler(t)
	source := "map \"🎬 films\" {\n let title = this.title\n if true { root.title = $title }\n}\nlet title = this\nmeta name = \"x\"\nroot.\"a=b\" = $title\n"
	openFeature(t, h, uri, source)
	got, err := h.DocumentSymbol(context.Background(), &protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}})
	require.NoError(t, err)
	require.Len(t, got, 4)
	require.Equal(t, "🎬 films", got[0].Name)
	require.Equal(t, protocol.SymbolKindFunction, got[0].Kind)
	require.Equal(t, 14, got[0].SelectionRange.End.Character)
	require.Len(t, got[0].Children, 2)
	require.Equal(t, "title", got[0].Children[0].Name)
	require.Equal(t, "root.title", got[0].Children[1].Name)
	require.Equal(t, "title", got[1].Name)
	require.Equal(t, "meta name", got[2].Name)
	require.Equal(t, `root."a=b"`, got[3].Name)
	require.Equal(t, 3, got[0].Range.End.Line)
}

func TestDocumentSymbolsAndFoldsYAML(t *testing.T) {
	h, _ := featureHandler(t)
	uri := fileURI(filepath.Join(h.workspaceRoot, "pipeline.yaml"))
	source := "mapping: |\n  map transform {\n    root = this\n  }\n  root = this.apply(\"transform\")\nmessage: '${! this.name }'\nother: unchanged\n"
	openFeature(t, h, uri, source)
	got, err := h.DocumentSymbol(context.Background(), &protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "transform", got[0].Name)
	require.Equal(t, protocol.Position{Line: 1, Character: 6}, got[0].SelectionRange.Start)
	require.Len(t, got[0].Children, 1)
	require.Equal(t, 2, got[0].Children[0].Range.Start.Line)
	folds, err := h.FoldingRange(context.Background(), &protocol.FoldingRangeParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}})
	require.NoError(t, err)
	require.Len(t, folds, 2)
	require.Equal(t, 0, folds[0].StartLine)
	require.Equal(t, 4, folds[0].EndLine)
	require.Equal(t, 1, folds[1].StartLine)
	require.Equal(t, 2, folds[1].EndLine)
}

func TestFoldingStructuresCommentsAndImports(t *testing.T) {
	h, uri := featureHandler(t)
	source := "# first\n# second\nimport \"a.blobl\"\nimport \"b.blobl\"\nmap transform {\n root = {\n  \"a\": [\n   1,\n   2\n  ]\n }\n}\n"
	openFeature(t, h, uri, source)
	folds, err := h.FoldingRange(context.Background(), &protocol.FoldingRangeParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}})
	require.NoError(t, err)
	require.Len(t, folds, 5)
	require.Equal(t, protocol.FoldingRangeKindComment, *folds[0].Kind)
	require.Equal(t, protocol.FoldingRangeKindImports, *folds[1].Kind)
	require.Equal(t, 4, folds[2].StartLine)
	require.Equal(t, 10, folds[2].EndLine)
	require.Equal(t, 5, folds[3].StartLine)
	require.Equal(t, 9, folds[3].EndLine)
	require.Equal(t, 6, folds[4].StartLine)
	require.Equal(t, 8, folds[4].EndLine)
}

func TestEditorFeatureWireHandlers(t *testing.T) {
	h, uri := featureHandler(t)
	harness := servertest.New(t, h)
	require.NoError(t, harness.DidOpen(uri, "bloblang", "map convert {\nroot = this\n}\nroot = range(0, "))
	got, err := harness.SignatureHelp(uri, 3, len("root = range(0, "))
	require.NoError(t, err)
	require.NotNil(t, got)
	symbols, err := harness.DocumentSymbol(uri)
	require.NoError(t, err)
	require.NotEmpty(t, symbols)
	folds, err := harness.FoldingRange(uri)
	require.NoError(t, err)
	require.NotEmpty(t, folds)
}
