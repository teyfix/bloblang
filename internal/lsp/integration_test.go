package lsp_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teyfix/bloblang-lsp/internal/fileuri"
	"github.com/teyfix/bloblang-lsp/internal/testutil"
)

func TestIntegrationMethodCompletion(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "file:///tmp/method.blobl"
	h.OpenDocument(uri, "root = this.")
	items := h.GetCompletions(uri, protocol.Position{Line: 0, Character: len("root = this.")})
	require.NotEmpty(t, items)
	for _, item := range items {
		require.NotNil(t, item.Kind)
		assert.Equal(t, protocol.CompletionItemKindMethod, *item.Kind)
	}
	testutil.AssertCompletionContains(t, items, "string")
}

func TestIntegrationFunctionCompletion(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "file:///tmp/function.blobl"
	h.OpenDocument(uri, "root = ")
	items := h.GetCompletions(uri, protocol.Position{Line: 0, Character: len("root = ")})
	require.NotEmpty(t, items)
	for _, item := range items {
		require.NotNil(t, item.Kind)
		assert.Equal(t, protocol.CompletionItemKindFunction, *item.Kind)
	}
	testutil.AssertCompletionContains(t, items, "json")
}

func TestIntegrationFunctionHover(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "file:///tmp/hover.blobl"
	h.OpenDocument(uri, "root = json(\"name\")")
	hover := h.GetHover(uri, protocol.Position{Line: 0, Character: len("root = js")})
	require.NotNil(t, hover)
	assert.Contains(t, hover.Contents.Value(), "**[json]")
}

func TestIntegrationDiagnostics(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "file:///tmp/diag.blobl"
	h.OpenDocument(uri, "root = (")
	diags := h.WaitForDiagnostics(uri, time.Second)
	require.NotEmpty(t, diags)
	assert.Equal(t, protocol.SeverityError, *diags[0].Severity)

	h.Server.ClearDiagnostics()
	h.ChangeDocument(uri, "root = \"ok\"")
	diags = h.WaitForDiagnostics(uri, time.Second)
	assert.NotContains(t, diagnosticMessages(diags), "Syntax error")
}

func TestIntegrationImmediateDiagnostics(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "file:///tmp/immediate.blobl"
	h.OpenDocument(uri, "root = (")

	// Wait for transport delivery
	diags := h.WaitForDiagnostics(uri, time.Second)
	require.NotEmpty(t, diags)
	assert.Equal(t, protocol.SeverityError, *diags[0].Severity)

	h.Server.ClearDiagnostics()
	h.ChangeDocument(uri, "root = \"ok\"")

	// Wait for diagnostics to be cleared
	diags = h.WaitForDiagnostics(uri, time.Second)
	assert.NotContains(t, diagnosticMessages(diags), "Syntax error")
}

func TestIntegrationCloseClearsDiagnostics(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "file:///tmp/close.blobl"
	h.OpenDocument(uri, "root = (")
	diags := h.WaitForDiagnostics(uri, time.Second)
	require.NotEmpty(t, diags)
	h.Server.ClearDiagnostics()
	h.CloseDocument(uri)
	diags = h.WaitForDiagnostics(uri, time.Second)
	assert.NotContains(t, diagnosticMessages(diags), "Syntax error")
}

func TestIntegrationImportHints(t *testing.T) {
	h := testutil.NewHarness(t)
	dir := filepath.ToSlash(t.TempDir())
	uri := fileuri.FromPath(filepath.Join(dir, "map.blobl"))
	h.OpenDocument(uri, "import \"foo.blobl\"\nroot = \"ok\"")
	diags := h.WaitForDiagnostics(uri, time.Second)
	testutil.AssertDiagnosticAt(t, diags, 0, filepath.Join(filepath.FromSlash(dir), "foo.blobl"))
}

func TestIntegrationUntitledImportHints(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "untitled:///draft"
	h.OpenDocument(uri, "import \"foo.blobl\"\nroot = \"ok\"")
	diags := h.WaitForDiagnostics(uri, time.Second)
	require.NotEmpty(t, diags)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	testutil.AssertDiagnosticAt(t, diags, 0, filepath.Join(home, "foo.blobl"))
	testutil.AssertDiagnosticAt(t, diags, 0, "Import base directory")
}

func TestIntegrationInlayHintsAndCodeLens(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "file:///tmp/inlay.blobl"
	h.OpenDocument(uri, "#!input {\"name\":\"alice\"}\nroot = this.name")

	// Inlay hint shows the OUTPUT after the line: root = this.name → "alice"
	hints := h.GetInlayHints(uri)
	testutil.AssertInlayHintAt(t, hints, 1, `= "alice"`)

	// Under the new design, small messages don't generate code lenses
	lenses := h.GetCodeLenses(uri)
	assert.Empty(t, lenses)
}

func TestIntegrationNoSampleInlayHintsEmpty(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "file:///tmp/no-sample.blobl"
	h.OpenDocument(uri, "root = this.name")
	assert.Empty(t, h.GetInlayHints(uri))
}

func TestIntegrationTruncatedCodeLensCommand(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "file:///tmp/truncated.blobl"
	h.OpenDocument(uri, `
#!input {"name":"abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz"}
root = this.name`[1:])
	lenses := h.GetCodeLenses(uri)
	require.Len(t, lenses, 4)
	require.NotNil(t, lenses[0].Command)
	assert.Contains(t, lenses[0].Command.Title, "Show Input")
	assert.Equal(t, "bloblang-lsp.showResult", lenses[0].Command.Command)

	require.NotNil(t, lenses[1].Command)
	assert.Equal(t, "Show Input Schema", lenses[1].Command.Title)
	assert.Equal(t, "bloblang-lsp.showSchema", lenses[1].Command.Command)
	require.Len(t, lenses[1].Command.Arguments, 2)
	require.NotNil(t, lenses[2].Command)
	assert.Contains(t, lenses[2].Command.Title, "Show Output")
	assert.Equal(t, "bloblang-lsp.showResult", lenses[2].Command.Command)
	require.NotNil(t, lenses[3].Command)
	assert.Equal(t, "Show Output Schema", lenses[3].Command.Title)
	assert.Equal(t, "bloblang-lsp.showSchema", lenses[3].Command.Command)

	var full map[string]interface{}
	require.NoError(t, json.Unmarshal(lenses[0].Command.Arguments[0], &full))
	assert.Contains(t, full["name"].(string), "abcdefghijklmnopqrstuvwxyz")
}

func TestIntegrationRootHover(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "file:///tmp/root-hover.blobl"
	h.OpenDocument(uri, "#!input {\"name\":\"alice\"}\nroot = this.name")
	hover := h.GetHover(uri, protocol.Position{Line: 1, Character: 1})
	require.NotNil(t, hover)
	assert.Contains(t, hover.Contents.Value(), "```yaml")
	assert.Contains(t, hover.Contents.Value(), `alice`)
}

func TestIntegrationRootHoverNoSample(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "file:///tmp/root-hover-no-sample.blobl"
	h.OpenDocument(uri, "root = this.name")
	hover := h.GetHover(uri, protocol.Position{Line: 0, Character: 1})
	assert.Nil(t, hover)
}

func TestIntegrationRootHoverNonAssignment(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "file:///tmp/root-hover-non-assignment.blobl"
	h.OpenDocument(uri, "let root = \"value\"")
	hover := h.GetHover(uri, protocol.Position{Line: 0, Character: 5})
	assert.Nil(t, hover)
}

func TestIntegrationSubPathInlayHintsAndCodeLens(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "file:///tmp/subpath.blobl"
	// sub-path assignment: root.name = this.name
	h.OpenDocument(uri, "#!input {\"name\":\"alice\"}\nroot.name = this.name")

	// inlay hint should appear on line 1 showing the output {"name":"alice"}
	hints := h.GetInlayHints(uri)
	testutil.AssertInlayHintAt(t, hints, 1, `alice`)

	lenses := h.GetCodeLenses(uri)
	assert.Empty(t, lenses)
}

func TestIntegrationCumulativeLensesAndHints(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "file:///tmp/cumulative.blobl"
	doc := "#!input {\"name\":\"alice\",\"age\":30}\nroot.name = this.name\nroot.age = this.age"
	h.OpenDocument(uri, doc)

	// inlay hints: line 1 shows output after first assignment → {"name":"alice"}
	//              line 2 shows output after both assignments → {"age":30,"name":"alice"}
	hints := h.GetInlayHints(uri)
	testutil.AssertInlayHintAt(t, hints, 1, `alice`)
	testutil.AssertInlayHintAt(t, hints, 2, `"age"`)

	lenses := h.GetCodeLenses(uri)
	assert.Empty(t, lenses)
}

func TestIntegrationSampleFromCodeLens(t *testing.T) {
	h := testutil.NewHarness(t)
	dir := t.TempDir()
	samplePath := filepath.Join(dir, "my_sample.json")
	require.NoError(t, os.WriteFile(samplePath, []byte(`{"$bloblang":{"input":{"name":"bob"}}}`), 0644))

	uri := "file:///" + filepath.ToSlash(dir) + "/test.blobl"
	doc := fmt.Sprintf("#!sample_from %s\nroot = this.name", "my_sample.json")
	h.OpenDocument(uri, doc)

	lenses := h.GetCodeLenses(uri)
	require.Len(t, lenses, 1)
	assert.Equal(t, "[Open Sample]", lenses[0].Command.Title)
	assert.Equal(t, "bloblang-lsp.openFile", lenses[0].Command.Command)

	// Inlay hints should work too
	hints := h.GetInlayHints(uri)
	testutil.AssertInlayHintAt(t, hints, 0, `bob`) // hint at sample_from comment end
}

func TestIntegrationMultilineInlayHint(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "file:///tmp/multiline.blobl"
	// Multi-line assignment: root.msg spans lines 1-3
	doc := "#!input {\"message\":\"hello world\"}\nroot.msg = this.\n  message.\n  replace(\"hello\", \"good morning\")"
	h.OpenDocument(uri, doc)

	hints := h.GetInlayHints(uri)
	// Inlay hint should appear on the LAST line of the statement (line 3), not line 1
	testutil.AssertInlayHintAt(t, hints, 3, "good morning")
}

func TestIntegrationExecDiagnostic(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "file:///tmp/exec-diag.blobl"
	// this.name is null (not in sample) → calling .uppercase() on null errors at runtime
	h.OpenDocument(uri, "#!input {}\nroot = this.name.uppercase()")

	// Trigger inlay hints — this runs ExecuteCumulative which will fail and publish a warning diagnostic.
	h.GetInlayHints(uri)

	// Execution warnings are delivered as an asynchronous notification.
	diags := h.WaitForDiagnostics(uri, time.Second)
	require.NotEmpty(t, diags)

}

func TestIntegrationMapDeclarationCumulative(t *testing.T) {
	h := testutil.NewHarness(t)
	uri := "file:///tmp/map-cumulative.blobl"
	doc := `
#!input {"user": {"first_name": "john"}}
map normalize_name {
  root = this.uppercase()
}
root.name = this.user.first_name.apply("normalize_name")`[1:]
	h.OpenDocument(uri, doc)

	// Inlay hints should work perfectly on line 4 (row 4): output = {"name":"JOHN"}
	hints := h.GetInlayHints(uri)
	testutil.AssertInlayHintAt(t, hints, 4, `JOHN`)

	// Verification of diagnostics: no compilation/execution errors should exist
	diags := h.Server.Diagnostics(protocol.DocumentURI(uri))
	assert.NotContains(t, diagnosticMessages(diags), "Syntax error")
}

func diagnosticMessages(ds []protocol.Diagnostic) []string {
	out := []string{}
	for _, d := range ds {
		if d.Severity != nil && *d.Severity == protocol.SeverityError {
			out = append(out, d.Message)
		}
	}
	return out
}
