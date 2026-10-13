package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func ignoreFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeInput(t, filepath.Join(dir, ".git"), "gitdir: ignored-marker\n")
	t.Chdir(dir)
	return dir
}

func TestDiscoveryIgnoreFilesAndNestedNegation(t *testing.T) {
	dir := ignoreFixture(t)
	writeInput(t, filepath.Join(dir, ".gitignore"), "# comment\n*.skip.blobl\n!keep.skip.blobl\n/anchored.blobl\nblocked/\n\\#literal.blobl\n")
	writeInput(t, filepath.Join(dir, ".bloblangignore"), "tool-only.blobl\n")
	writeInput(t, filepath.Join(dir, "nested", ".gitignore"), "!nested.skip.blobl\n")
	writeInput(t, filepath.Join(dir, "blocked", ".gitignore"), "!hidden.blobl\n")
	visible := []string{"mapping.blobl", "keep.skip.blobl", "nested/nested.skip.blobl", "nested/anchored.blobl"}
	ignored := []string{"bad.skip.blobl", "anchored.blobl", "blocked/hidden.blobl", "tool-only.blobl", "#literal.blobl"}
	for _, path := range visible {
		writeInput(t, filepath.Join(dir, filepath.FromSlash(path)), "root=this\n")
	}
	for _, path := range ignored {
		writeInput(t, filepath.Join(dir, filepath.FromSlash(path)), "root = [\n")
	}
	for _, paths := range [][]string{{dir}, {"**/*.blobl"}} {
		code, out, stderr := invoke(t, "", append([]string{"fmt", "--write"}, paths...)...)
		require.Zero(t, code, stderr)
		require.Empty(t, out)
	}
	for _, path := range ignored {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
		require.NoError(t, err)
		require.Equal(t, "root = [\n", string(data))
	}
	code, out, stderr := invoke(t, "", "lint", "--format", "json", "**/*.blobl")
	require.Zero(t, code, stderr)
	require.Equal(t, "[]\n", out)
	code, out, stderr = invoke(t, "", "lint", "--no-ignore", "--format", "json", "**/*.blobl")
	require.Equal(t, 1, code, stderr)
	for _, path := range ignored {
		require.Contains(t, out, filepath.Base(path))
	}
	code, out, stderr = invoke(t, "", "lint", "--format", "json", "bad.skip.blobl")
	require.Equal(t, 1, code, stderr)
	require.Contains(t, out, "bad.skip.blobl", "explicit files bypass ignored patterns")
}

func TestDiscoveryConfigExcludesAndRespectGitignore(t *testing.T) {
	dir := ignoreFixture(t)
	writeInput(t, filepath.Join(dir, ".bloblangrc.json"), `{"files":{"exclude":["*.excluded.blobl","!keep.excluded.blobl","/generated/"]}}`)
	writeInput(t, filepath.Join(dir, ".gitignore"), "git-only.blobl\n")
	writeInput(t, filepath.Join(dir, ".bloblangignore"), "tool-only.blobl\n")
	writeInput(t, filepath.Join(dir, "nested", ".bloblangrc.json"), `{"files":{"exclude":["/local.blobl"],"respectGitignore":false}}`)
	for _, path := range []string{"keep.excluded.blobl", "nested/git-only.blobl", "nested/parent.excluded.blobl", "nested/deeper/local.blobl"} {
		writeInput(t, filepath.Join(dir, filepath.FromSlash(path)), "root = this\n")
	}
	for _, path := range []string{"bad.excluded.blobl", "generated/bad.blobl", "git-only.blobl", "tool-only.blobl", "nested/local.blobl", "nested/tool-only.blobl"} {
		writeInput(t, filepath.Join(dir, filepath.FromSlash(path)), "root = [\n")
	}
	for _, path := range []string{dir, "**/*.blobl"} {
		code, out, stderr := invoke(t, "", "lint", "--format", "json", path)
		require.Zero(t, code, stderr)
		require.Equal(t, "[]\n", out)
	}
	code, out, stderr := invoke(t, "", "lint", "--format", "json", "nested/git-only.blobl")
	require.Zero(t, code, stderr)
	require.Equal(t, "[]\n", out)
	code, out, stderr = invoke(t, "", "lint", "--no-ignore", "--format", "json", "**/*.blobl")
	require.Equal(t, 1, code, stderr)
	require.Contains(t, out, "bad.excluded.blobl")
	require.Contains(t, out, "tool-only.blobl")
}

func TestDiscoveryAllIgnoredAndValidationErrors(t *testing.T) {
	dir := ignoreFixture(t)
	writeInput(t, filepath.Join(dir, ".bloblangignore"), "*.blobl\n")
	writeInput(t, filepath.Join(dir, "bad.blobl"), "root = [\n")
	for _, args := range [][]string{{"fmt", "*.blobl"}, {"fmt", "--check", "**/*.blobl"}, {"fmt", "--write", dir}, {"lint", "--format", "json", "**/*.blobl"}} {
		code, out, stderr := invoke(t, "", args...)
		require.Zero(t, code, stderr)
		if args[0] == "lint" {
			require.Equal(t, "[]\n", out)
		} else {
			require.Empty(t, out)
		}
	}
	code, out, stderr := invoke(t, "", "fmt", "--no-ignore", "--check", "**/*.missing")
	require.Equal(t, 2, code)
	require.Empty(t, out)
	require.Contains(t, stderr, "matched no files")
	writeInput(t, filepath.Join(dir, ".bloblangrc.json"), "{")
	for _, path := range []string{dir, "**/*.blobl"} {
		code, out, stderr = invoke(t, "", "lint", path)
		require.Equal(t, 2, code)
		require.Empty(t, out)
		require.Contains(t, stderr, ".bloblangrc.json")
	}
	writeInput(t, filepath.Join(dir, ".bloblangrc.json"), `{"files":{"exclude":["**bad"]}}`)
	code, out, stderr = invoke(t, "", "lint", dir)
	require.Equal(t, 2, code)
	require.Empty(t, out)
	require.Contains(t, stderr, "files.exclude")
	writeInput(t, filepath.Join(dir, ".bloblangrc.json"), `{}`)
	writeInput(t, filepath.Join(dir, ".bloblangignore"), "**bad\n")
	code, out, stderr = invoke(t, "", "lint", dir)
	require.Equal(t, 2, code)
	require.Empty(t, out)
	require.Contains(t, stderr, ".bloblangignore")
	code, out, stderr = invoke(t, "", "lint", "--no-ignore", dir)
	require.Equal(t, 1, code, stderr)
	require.Contains(t, out, "bad.blobl")
}

func TestNoIgnoreRetainsHardSkips(t *testing.T) {
	dir := ignoreFixture(t)
	writeInput(t, filepath.Join(dir, "mapping.blobl"), "root = this\n")
	for _, skip := range []string{".git", ".cache", ".state", ".worktrees", ".volumes", "node_modules", "vendor", "target"} {
		if skip == ".git" {
			continue
		}
		writeInput(t, filepath.Join(dir, skip, "bad.blobl"), "root = [\n")
	}
	code, out, stderr := invoke(t, "", "lint", "--no-ignore", "--format", "json", "**/*.blobl")
	require.Zero(t, code, stderr)
	require.Equal(t, "[]\n", out)
	require.Empty(t, strings.TrimSpace(stderr))
}

func TestIgnoredDirectoriesPrunedBeforeLocalSettings(t *testing.T) {
	dir := ignoreFixture(t)
	writeInput(t, filepath.Join(dir, ".bloblangrc.json"), `{"files":{"exclude":["generated/"]}}`)
	writeInput(t, filepath.Join(dir, ".gitignore"), "blocked/\n")
	writeInput(t, filepath.Join(dir, ".bloblangignore"), "tool-blocked/\n")
	writeInput(t, filepath.Join(dir, "visible.blobl"), "root = this\n")
	writeInput(t, filepath.Join(dir, "blocked", ".bloblangrc.json"), `{"files":{"respectGitignore":false}}`)
	writeInput(t, filepath.Join(dir, "generated", ".bloblangrc.json"), `{}`)
	writeInput(t, filepath.Join(dir, "tool-blocked", ".bloblangrc.json"), "{")
	for _, blocked := range []string{"blocked", "generated", "tool-blocked"} {
		writeInput(t, filepath.Join(dir, blocked, ".bloblangignore"), "**bad\n")
		writeInput(t, filepath.Join(dir, blocked, "bad.blobl"), "root = [\n")
		writeInput(t, filepath.Join(dir, blocked, "deep", "bad.blobl"), "root = [\n")
	}
	for _, target := range []string{dir, "**/*.blobl", "blocked/**/*.blobl", "generated/**/*.blobl", "tool-blocked/**/*.blobl", "blocked/deep/**/*.blobl", "generated/deep/**/*.blobl", "tool-blocked/deep/**/*.blobl"} {
		code, out, stderr := invoke(t, "", "lint", "--format", "json", target)
		require.Zero(t, code, stderr)
		require.Equal(t, "[]\n", out)
	}
	// Explicit files still expose their own configuration errors deliberately.
	code, out, stderr := invoke(t, "", "lint", "tool-blocked/bad.blobl")
	require.Equal(t, 2, code)
	require.Empty(t, out)
	require.Contains(t, stderr, ".bloblangrc.json")
}

func TestGlobProjectRootDoesNotReadParentSettings(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "project")
	writeInput(t, filepath.Join(parent, ".bloblangrc.json"), "{")
	writeInput(t, filepath.Join(parent, ".gitignore"), "project/\n")
	writeInput(t, filepath.Join(project, ".git"), "gitdir: ignored-marker\n")
	writeInput(t, filepath.Join(project, "mapping.blobl"), "root = this\n")
	code, out, stderr := invoke(t, "", "lint", "--format", "json", filepath.Join(project, "**", "*.blobl"))
	require.Zero(t, code, stderr)
	require.Equal(t, "[]\n", out)
}

func TestGlobHardSkippedAncestorsInLiteralPrefix(t *testing.T) {
	dir := ignoreFixture(t)
	for _, skip := range []string{"node_modules", ".cache", ".state", ".worktrees", ".volumes", "vendor", "target"} {
		writeInput(t, filepath.Join(dir, skip, "child", "bad.blobl"), "root = [\n")
		for _, flag := range [][]string{nil, {"--no-ignore"}} {
			args := append([]string{"lint"}, flag...)
			args = append(args, filepath.Join(skip, "child", "**", "*.blobl"))
			code, out, stderr := invoke(t, "", args...)
			require.Equal(t, 2, code, stderr)
			require.Empty(t, out)
			require.Contains(t, stderr, "matched no files")
		}
	}
}
