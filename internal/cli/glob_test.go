package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuotedRecursiveGlobsFormatAndLint(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	paths := []string{
		filepath.Join(dir, "root.blobl"),
		filepath.Join(dir, "nested", "mapping.blobl"),
		filepath.Join(dir, "nested", "deep folder", "has spaces.blobl"),
	}
	for _, path := range paths {
		writeInput(t, path, "root=this\n")
	}
	for _, skip := range []string{".git", ".volumes", "node_modules", "vendor", "target"} {
		writeInput(t, filepath.Join(dir, "nested", skip, "invalid.blobl"), "root = [\n")
	}
	writeInput(t, filepath.Join(dir, "ignored.txt"), "root = [\n")
	sort.Strings(paths)
	code, out, stderr := invoke(t, "", "fmt", "--check", "**/*.blobl", "nested/**/*.blobl", paths[0])
	require.Equal(t, 1, code, stderr)
	require.Empty(t, stderr)
	require.Equal(t, strings.Join(paths, "\n")+"\n", out)
	code, out, stderr = invoke(t, "", "lint", "--format", "json", "**/*.blobl")
	require.Equal(t, 1, code, stderr)
	require.Contains(t, out, `"severity":"error"`)
	code, out, stderr = invoke(t, "", "fmt", "--write", "**/*.blobl")
	require.Zero(t, code, out, stderr)
	require.Empty(t, out)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "root = this\n", string(data))
	}
	// Native separators and absolute patterns support portable Windows callers.
	code, out, stderr = invoke(t, "", "fmt", "--check", filepath.Join(dir, "**", "*.blobl"))
	require.Zero(t, code, out, stderr)
	require.Empty(t, out)
	code, out, stderr = invoke(t, "", "fmt", "--check", "nested/**/has spaces.blobl")
	require.Zero(t, code, out, stderr)
	require.Empty(t, out)
}

func TestGlobErrorsAndStdoutCount(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeInput(t, filepath.Join(dir, "one.blobl"), "root=this\n")
	writeInput(t, filepath.Join(dir, "nested", "two.blobl"), "root=this\n")
	for _, pattern := range []string{"missing/**/*.blobl", "**/*.missing", "*.blobl[", "{one,two"} {
		code, out, stderr := invoke(t, "", "fmt", "--write", "one.blobl", pattern)
		require.Equal(t, 2, code, pattern)
		require.Empty(t, out)
		require.Contains(t, stderr, "glob pattern")
		code, out, stderr = invoke(t, "", "lint", "--format", "json", pattern)
		require.Equal(t, 2, code, pattern)
		require.Empty(t, out)
		require.Contains(t, stderr, "glob pattern")
	}
	data, err := os.ReadFile(filepath.Join(dir, "one.blobl"))
	require.NoError(t, err)
	require.Equal(t, "root=this\n", string(data), "invalid later patterns must fail before any write")
	code, out, stderr := invoke(t, "", "fmt", "**/*.blobl")
	require.Equal(t, 2, code)
	require.Empty(t, out)
	require.Contains(t, stderr, "multiple files requires --write or --check")
	code, out, stderr = invoke(t, "", "fmt", "nested/*.blobl")
	require.Zero(t, code, out, stderr)
	require.Equal(t, "root = this\n", out)
	// Counting after dedup also accepts repeated shell-expanded/literal inputs.
	code, out, stderr = invoke(t, "", "fmt", "nested/*.blobl", "nested/two.blobl")
	require.Zero(t, code, out, stderr)
	require.Equal(t, "root = this\n", out)
}

func TestGlobLiteralNamesDirectoriesAndLintFix(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// Brackets are valid filename characters on every supported platform.
	literal := filepath.Join(dir, "literal[.blobl")
	writeInput(t, literal, "root=this\n")
	code, out, stderr := invoke(t, "", "fmt", literal)
	require.Zero(t, code, out, stderr)
	require.Equal(t, "root = this\n", out)
	if runtime.GOOS != "windows" {
		star := filepath.Join(dir, "literal*.blobl")
		writeInput(t, star, "root=this\n")
		code, out, stderr = invoke(t, "", "fmt", star)
		require.Zero(t, code, out, stderr)
		require.Equal(t, "root = this\n", out)
	}
	a := filepath.Join(dir, "inputs one", "mapping.blobl")
	b := filepath.Join(dir, "inputs two", "nested", "mapping.blobl")
	for _, path := range []string{a, b} {
		writeInput(t, path, "root = this.without(\"a\").without(\"b\")\n")
	}
	code, out, stderr = invoke(t, "", "lint", "--format", "json", "inputs */**/*.blobl", "inputs one")
	require.Equal(t, 1, code, stderr)
	var diagnostics []fileDiagnostic
	require.NoError(t, json.Unmarshal([]byte(out), &diagnostics))
	require.Len(t, diagnostics, 2)
	require.Equal(t, a, diagnostics[0].File)
	require.Equal(t, b, diagnostics[1].File)
	code, out, stderr = invoke(t, "", "lint", "--fix", "--format", "json", "inputs *", "inputs */**/*.blobl")
	require.Zero(t, code, out, stderr)
	require.Equal(t, "[]\n", out)
	for _, path := range []string{a, b} {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "root = this.without(\"a\", \"b\")\n", string(data))
	}
}

func TestGlobSkipsSymlinksAndExcludedExplicitSegments(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	real := filepath.Join(dir, "real", "mapping.blobl")
	writeInput(t, real, "root=this\n")
	external := filepath.Join(t.TempDir(), "external")
	writeInput(t, filepath.Join(external, "invalid.blobl"), "root = [\n")
	if err := os.Symlink(external, filepath.Join(dir, "linked-dir")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	require.NoError(t, os.Symlink(real, filepath.Join(dir, "linked.blobl")))
	writeInput(t, filepath.Join(dir, "node_modules", "invalid.blobl"), "root = [\n")
	code, out, stderr := invoke(t, "", "fmt", "--check", "**/*.blobl")
	require.Equal(t, 1, code, stderr)
	require.Equal(t, real+"\n", out)
	for _, pattern := range []string{"linked-dir/*.blobl", "*/invalid.blobl", "{node_modules,linked-dir}/**/*.blobl"} {
		code, out, stderr = invoke(t, "", "fmt", "--check", pattern)
		require.Equal(t, 2, code, pattern)
		require.Empty(t, out)
		require.Contains(t, stderr, "matched no files")
	}
	// Literal files remain supported through ancestor aliases.
	alias := filepath.Join(dir, "alias")
	require.NoError(t, os.Symlink(filepath.Dir(real), alias))
	code, out, stderr = invoke(t, "", "fmt", filepath.Join(alias, "mapping.blobl"))
	require.Zero(t, code, out, stderr)
	require.Equal(t, "root = this\n", out)
}
