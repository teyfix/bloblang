package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/owenrumney/go-lsp/servertest"
	"github.com/stretchr/testify/require"
	"github.com/teyfix/bloblang-lsp/internal/schema"
	"go.yaml.in/yaml/v3"
)

func schemaPreviewDirectory(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "preview space #")
	require.NoError(t, os.Mkdir(dir, 0700))
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	return dir
}

func schemaPreviewRequests(t *testing.T, client *servertest.Harness) []protocol.ShowDocumentParams {
	t.Helper()
	var shown []protocol.ShowDocumentParams
	for _, request := range client.ClientRequests() {
		if request.Method == "window/showDocument" {
			var params protocol.ShowDocumentParams
			require.NoError(t, json.Unmarshal(request.Params, &params))
			shown = append(shown, params)
		}
	}
	return shown
}

func requireEmptySchemaPreviewDirectory(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries, "failed previews must not retain temporary files")
}

func TestSchemaPreviewUsesOriginFormatAndEscapedFileURI(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			h, uri := featureHandler(t)
			// A nested configuration proves the origin URI is forwarded to the
			// preview renderer rather than falling back to the workspace root.
			project := filepath.Join(h.workspaceRoot, "nested")
			require.NoError(t, os.Mkdir(project, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(project, ".bloblangrc.json"), []byte(`{"preview":{"format":"`+format+`"}}`), 0600))
			uri = fileURI(filepath.Join(project, "mapping.blobl"))
			dir := schemaPreviewDirectory(t)
			client := servertest.New(t, h)
			client.SetClientResponse("window/showDocument", protocol.ShowDocumentResult{Success: true})

			result, err := client.ExecuteCommand("bloblang-lsp.showSchema", []json.RawMessage{
				json.RawMessage(`[{"id":1,"name":null},{"id":2,"name":"Ada","active":true}]`), rawJSON(string(uri)),
			})
			require.NoError(t, err)
			require.JSONEq(t, `{"success":true}`, string(result))
			shown := schemaPreviewRequests(t, client)
			require.Len(t, shown, 1)
			require.NotNil(t, shown[0].TakeFocus)
			require.True(t, *shown[0].TakeFocus)
			u, err := url.Parse(string(shown[0].URI))
			require.NoError(t, err)
			require.Equal(t, "file", u.Scheme)
			require.Empty(t, u.Fragment, "a # in the temporary path must be escaped")
			require.Contains(t, string(shown[0].URI), "%20")
			require.Contains(t, string(shown[0].URI), "%23")
			path, err := uriToPath(protocol.DocumentURI(shown[0].URI))
			require.NoError(t, err)
			require.Equal(t, dir, filepath.Dir(path))
			require.Equal(t, "."+format, filepath.Ext(path))
			body, err := os.ReadFile(path)
			require.NoError(t, err)
			var document any
			if format == "json" {
				require.NoError(t, json.Unmarshal(body, &document))
			} else {
				require.NoError(t, yaml.Unmarshal(body, &document))
			}
			encoded, err := json.Marshal(document)
			require.NoError(t, err)
			var preview schema.Result
			require.NoError(t, json.Unmarshal(encoded, &preview))
			require.JSONEq(t, `{"elements":{"properties":{"id":{"type":"float64"},"name":{"type":"string","nullable":true}},"optionalProperties":{"active":{"type":"boolean"}}}}`, string(preview.Schema))
			require.Equal(t, "jtd", preview.Meta.Format)
			require.True(t, preview.Meta.SampleBased)
			require.False(t, preview.Meta.EnumInference)
			require.NotEmpty(t, preview.Meta.Warnings)
		})
	}
}

func TestSchemaPreviewRejectsInvalidRequestsWithoutFiles(t *testing.T) {
	h, uri := featureHandler(t)
	dir := schemaPreviewDirectory(t)
	client := servertest.New(t, h)
	client.SetClientResponse("window/showDocument", protocol.ShowDocumentResult{Success: true})
	for _, test := range []struct {
		name string
		args []json.RawMessage
	}{
		{"missing input", nil},
		{"too many arguments", []json.RawMessage{json.RawMessage(`{}`), rawJSON(string(uri)), json.RawMessage(`null`)}},
		{"invalid origin", []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`42`)}},
		{"invalid JSON", []json.RawMessage{json.RawMessage(`{"unfinished":`)}},
		{"multiple values", []json.RawMessage{json.RawMessage(`{} {}`)}},
		{"unsupported inference", []json.RawMessage{json.RawMessage(`[[],1,"x"]`)}},
		{"oversized input", []json.RawMessage{rawJSON(strings.Repeat("x", schema.MaxBytes))}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := h.ExecuteCommand(ctx, &protocol.ExecuteCommandParams{Command: "bloblang-lsp.showSchema", Arguments: test.args})
			require.Error(t, err)
			require.Empty(t, schemaPreviewRequests(t, client))
			requireEmptySchemaPreviewDirectory(t, dir)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.ExecuteCommand(ctx, &protocol.ExecuteCommandParams{Command: "bloblang-lsp.showSchema", Arguments: []json.RawMessage{json.RawMessage(`{}`)}})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, schemaPreviewRequests(t, client))
	requireEmptySchemaPreviewDirectory(t, dir)
}

func TestSchemaPreviewCleansUpClientFailures(t *testing.T) {
	for _, failure := range []string{"request error", "unsuccessful response"} {
		t.Run(failure, func(t *testing.T) {
			h, _ := featureHandler(t)
			dir := schemaPreviewDirectory(t)
			client := servertest.New(t, h)
			if failure == "request error" {
				client.SetClientError("window/showDocument", errors.New("editor cannot open preview"))
			} else {
				client.SetClientResponse("window/showDocument", protocol.ShowDocumentResult{Success: false})
			}
			_, err := client.ExecuteCommand("bloblang-lsp.showSchema", []json.RawMessage{json.RawMessage(`{"id":1}`)})
			require.Error(t, err)
			require.Len(t, schemaPreviewRequests(t, client), 1)
			requireEmptySchemaPreviewDirectory(t, dir)
		})
	}
}

func TestSchemaPreviewTempFailureDoesNotOpenDocument(t *testing.T) {
	h, _ := featureHandler(t)
	dir := schemaPreviewDirectory(t)
	client := servertest.New(t, h)
	client.SetClientResponse("window/showDocument", protocol.ShowDocumentResult{Success: true})
	missing := filepath.Join(dir, "missing directory")
	t.Setenv("TMPDIR", missing)
	t.Setenv("TMP", missing)
	t.Setenv("TEMP", missing)
	_, err := client.ExecuteCommand("bloblang-lsp.showSchema", []json.RawMessage{json.RawMessage(`{"id":1}`)})
	require.Error(t, err)
	require.Empty(t, schemaPreviewRequests(t, client))
	requireEmptySchemaPreviewDirectory(t, dir)
}

func TestSchemaCodeLensDefersInferenceUntilCommand(t *testing.T) {
	h, uri := featureHandler(t)
	h.config.MaxInlineResultBytes = 1
	dir := schemaPreviewDirectory(t)
	client := servertest.New(t, h)
	const sample = `[[],1,"x"]`
	openFeature(t, h, uri, "#!input "+sample+"\nroot = this\n")
	lenses, err := h.CodeLens(context.Background(), &protocol.CodeLensParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}})
	require.NoError(t, err)
	var schemaLenses, resultLenses []protocol.CodeLens
	for _, lens := range lenses {
		require.NotNil(t, lens.Command)
		switch lens.Command.Command {
		case "bloblang-lsp.showSchema":
			schemaLenses = append(schemaLenses, lens)
		case "bloblang-lsp.showResult":
			resultLenses = append(resultLenses, lens)
		}
	}
	require.Len(t, schemaLenses, 2, "unsupported inference must not suppress input/output schema actions")
	require.Len(t, resultLenses, 2)
	for i, lens := range schemaLenses {
		require.Len(t, lens.Command.Arguments, 2)
		require.JSONEq(t, sample, string(lens.Command.Arguments[0]))
		require.JSONEq(t, string(rawJSON(string(uri))), string(lens.Command.Arguments[1]))
		require.Equal(t, resultLenses[i].Range, lens.Range)
		require.Equal(t, resultLenses[i].Command.Arguments, lens.Command.Arguments)
		_, err := client.ExecuteCommand(lens.Command.Command, lens.Command.Arguments)
		require.ErrorContains(t, err, "inferred schema rejects input")
	}
	require.Empty(t, schemaPreviewRequests(t, client))
	requireEmptySchemaPreviewDirectory(t, dir)
}
