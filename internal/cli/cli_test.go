package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func invoke(t *testing.T, input string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, strings.NewReader(input), &stdout, &stderr, BuildInfo{Version: "v1.2.3", DateBuilt: "today"})
	return code, stdout.String(), stderr.String()
}

func writeInput(t *testing.T, path, text string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte(text), 0640))
}

func TestHelpVersionAndUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"help", "fmt"}, {"fmt", "--help"}, {"lint", "-h"}, {"lsp", "--help"}} {
		code, out, stderr := invoke(t, "", args...)
		require.Zero(t, code, args)
		require.Contains(t, out, "Usage:")
		require.Empty(t, stderr)
	}
	code, out, stderr := invoke(t, "", "--version")
	require.Zero(t, code)
	require.Equal(t, "v1.2.3 (built today)\n", out)
	require.Empty(t, stderr)
	for _, args := range [][]string{
		{"missing"}, {"lsp", "file"}, {"fmt", "--unknown"}, {"fmt", "--write", "--check"},
		{"fmt", "--write"}, {"lint", "--fix", "-"}, {"fmt", "--tab-size", "0"}, {"fmt", "--tab-size", "9"},
		{"lint", "--format", "csv"}, {"lint", "-", "file.blobl"}, {"fmt", "a", "b"},
		{"lint", "--stdin-filepath", "virtual.blobl", "actual.blobl"},
	} {
		code, out, stderr := invoke(t, "root=this\n", args...)
		require.Equal(t, 2, code, args)
		require.Empty(t, out, args)
		require.NotEmpty(t, stderr, args)
	}
}

func TestFormatStdinFileAndYAML(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"fmt"}, {"fmt", "-"}} {
		code, out, stderr := invoke(t, "root={a:1,b:[2,3,]}\n", args...)
		require.Zero(t, code, stderr)
		require.Equal(t, "root = { a: 1, b: [2, 3] }\n", out)
		require.Empty(t, stderr)
	}
	path := filepath.Join(dir, "arbitrary.extension")
	writeInput(t, path, "root=this\n")
	code, out, stderr := invoke(t, "", "fmt", path)
	require.Zero(t, code, stderr)
	require.Equal(t, "root = this\n", out)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "root=this\n", string(data))
	yaml := "# host\nmapping: |-\n  root={a:1}\nnext: preserved # comment\n"
	code, out, stderr = invoke(t, yaml, "fmt", "--stdin-filepath", filepath.Join(dir, "virtual.yaml"))
	require.Zero(t, code, stderr)
	require.Equal(t, "# host\nmapping: |-\n  root = { a: 1 }\nnext: preserved # comment\n", out)
	code, out, stderr = invoke(t, "", "fmt", dir)
	require.Equal(t, 2, code)
	require.Empty(t, out)
	require.Contains(t, stderr, "requires --write or --check")
}

func TestFormatCheckWriteRecursionAndPermissions(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.blobl")
	z := filepath.Join(dir, "child", "z.bloblang")
	y := filepath.Join(dir, "pipeline.yml")
	for path, text := range map[string]string{a: "root=this\n", z: "root={a:1}\n", y: "mapping: |-\n  root=this\n"} {
		writeInput(t, path, text)
	}
	ignored := []string{"plain.txt", ".git/private.blobl", ".volumes/state.blobl", "node_modules/a.blobl", "vendor/a.blobl", "target/a.blobl"}
	for _, path := range ignored {
		writeInput(t, filepath.Join(dir, path), "root = [\n")
	}
	before, err := os.Stat(a)
	require.NoError(t, err)
	code, out, stderr := invoke(t, "", "fmt", "--check", z, dir, a)
	require.Equal(t, 1, code, stderr)
	require.Equal(t, strings.Join([]string{a, z, y}, "\n")+"\n", out)
	require.Empty(t, stderr)
	code, out, stderr = invoke(t, "", "fmt", "--write", dir, a)
	require.Zero(t, code, stderr)
	require.Empty(t, out)
	require.Empty(t, stderr)
	info, err := os.Stat(a)
	require.NoError(t, err)
	require.Equal(t, before.Mode().Perm(), info.Mode().Perm())
	code, out, stderr = invoke(t, "", "fmt", "--check", dir)
	require.Zero(t, code, stderr)
	require.Empty(t, out)
	for _, path := range ignored {
		data, err := os.ReadFile(filepath.Join(dir, path))
		require.NoError(t, err)
		require.Equal(t, "root = [\n", string(data))
	}
	entries, err := filepath.Glob(filepath.Join(dir, ".blobl-*"))
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestLintJSONFixAndSeverityExit(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.blobl")
	b := filepath.Join(dir, "b.yaml")
	writeInput(t, filepath.Join(dir, ".bloblangrc.json"), `{"lint":{"rules":{"correctness/variables/no-unused-let":"warn"}}}`)
	writeInput(t, a, "let unused = 1\nroot = this\n")
	writeInput(t, b, "mapping: |-\n  root = this.without(\"a\").without(\"b\")\nnext: preserved\n")
	code, out, stderr := invoke(t, "", "lint", "--format", "json", b, a, dir)
	require.Equal(t, 1, code, stderr)
	require.Empty(t, stderr)
	var findings []fileDiagnostic
	require.NoError(t, json.Unmarshal([]byte(out), &findings))
	require.Len(t, findings, 2)
	require.Equal(t, a, findings[0].File)
	require.Equal(t, "warn", findings[0].Severity)
	require.Equal(t, "correctness/variables/no-unused-let", findings[0].Code)
	require.GreaterOrEqual(t, findings[0].Line, 1)
	require.GreaterOrEqual(t, findings[0].Column, 1)
	code, out, stderr = invoke(t, "", "lint", "--fix", "--format", "json", b)
	require.Zero(t, code, stderr)
	require.Equal(t, "[]\n", out)
	data, err := os.ReadFile(b)
	require.NoError(t, err)
	require.Equal(t, "mapping: |-\n  root = this.without(\"a\", \"b\")\nnext: preserved\n", string(data))
	for _, severity := range []string{"info", "hint"} {
		writeInput(t, filepath.Join(dir, ".bloblangrc.json"), `{"lint":{"rules":{"correctness/variables/no-unused-let":"`+severity+`"}}}`)
		code, out, stderr = invoke(t, "", "lint", a)
		require.Zero(t, code, stderr)
		require.Contains(t, out, severity)
	}
}

func TestStdinConfigImportsAndErrors(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child")
	virtual := filepath.Join(child, "stdin.blobl")
	writeInput(t, filepath.Join(dir, ".bloblangrc.json"), `{"lint":{"rules":{"correctness/variables/no-unused-let":"error"}}}`)
	writeInput(t, filepath.Join(child, "shared.blobl"), "map identity { root = this }\n")
	text := "import \"shared.blobl\"\nlet unused = 1\nroot = this.apply(\"identity\")\n"
	code, out, stderr := invoke(t, text, "lint", "--stdin-filepath", virtual)
	require.Equal(t, 1, code, stderr)
	require.Contains(t, out, "error [correctness/variables/no-unused-let]")
	require.NotContains(t, out, "shared.blobl:")
	for _, args := range [][]string{{"fmt", "--stdin-filepath", virtual}, {"fmt", "--check", "--stdin-filepath", virtual}} {
		code, out, stderr = invoke(t, "root = [\n", args...)
		require.Equal(t, 2, code)
		require.Empty(t, out)
		require.NotEmpty(t, stderr)
	}
	code, out, stderr = invoke(t, "root = [\n", "lint", "--format", "json", "--stdin-filepath", virtual)
	require.Equal(t, 1, code, stderr)
	require.Contains(t, out, `"severity":"error"`)
	writeInput(t, filepath.Join(dir, ".bloblangrc.json"), "{")
	code, out, stderr = invoke(t, "root = this\n", "lint", "--stdin-filepath", virtual)
	require.Equal(t, 2, code)
	require.Empty(t, out)
	require.Contains(t, stderr, ".bloblangrc.json")
	code, out, stderr = invoke(t, "", "lint", filepath.Join(dir, "missing.blobl"))
	require.Equal(t, 2, code)
	require.Empty(t, out)
	require.NotEmpty(t, stderr)
}

func TestSymlinksAndChangedFiles(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	real := filepath.Join(realDir, "mapping.blobl")
	writeInput(t, real, "root=this\n")
	link := filepath.Join(dir, "link.blobl")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	code, out, stderr := invoke(t, "", "fmt", "--write", link)
	require.Equal(t, 2, code)
	require.Empty(t, out)
	require.Contains(t, stderr, "symlinks")
	writeInput(t, filepath.Join(dir, "external", "bad.blobl"), "root = [\n")
	require.NoError(t, os.Symlink(filepath.Join(dir, "external"), filepath.Join(realDir, "directory-link")))
	require.NoError(t, os.Symlink(realDir, filepath.Join(dir, "alias")))
	code, out, stderr = invoke(t, "", "fmt", "--write", filepath.Join(dir, "alias", "mapping.blobl"))
	require.Zero(t, code, stderr)
	require.Empty(t, out)
	code, _, stderr = invoke(t, "", "fmt", "--check", realDir)
	require.Zero(t, code, stderr)
	require.ErrorContains(t, replaceFile(real, "stale content", "root = this\n"), "changed while processing")
	data, err := os.ReadFile(real)
	require.NoError(t, err)
	require.Equal(t, "root = this\n", string(data))
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("writer failed") }

func TestCancelledContextAndOutputErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stderr bytes.Buffer
	code := Run(ctx, []string{"fmt"}, strings.NewReader("root=this\n"), io.Discard, &stderr, BuildInfo{})
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "context canceled")
	stderr.Reset()
	code = Run(context.Background(), []string{"fmt"}, strings.NewReader("root=this\n"), brokenWriter{}, &stderr, BuildInfo{})
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "writer failed")
}
