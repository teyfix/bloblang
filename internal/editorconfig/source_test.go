package editorconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSourceConfigBoundariesAndNearestPrecedence(t *testing.T) {
	for _, marker := range []string{"directory", "worktree file"} {
		t.Run(marker, func(t *testing.T) {
			outside := t.TempDir()
			root := filepath.Join(outside, "project")
			child := filepath.Join(root, "child")
			dir := filepath.Join(child, "nested")
			require.NoError(t, os.MkdirAll(dir, 0700))
			if marker == "directory" {
				require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0700))
			} else {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: elsewhere\n"), 0600))
			}
			require.NoError(t, os.WriteFile(filepath.Join(outside, ".bloblangrc.json"), []byte(`{"formatter":{"printWidth":200}}`), 0600))
			source := ResolveSource(dir, nil)
			require.Equal(t, root, source.Root)
			cfg, err := source.Config()
			require.NoError(t, err)
			require.Equal(t, Default(), cfg, "must not inherit settings above Git root")
			require.NoError(t, os.WriteFile(filepath.Join(root, ".bloblangrc.json"), []byte(`{"formatter":{"printWidth":100},"lint":{"enabled":false}}`), 0600))
			cfg, err = source.Config()
			require.NoError(t, err)
			require.Equal(t, 100, cfg.Formatter.PrintWidth)
			require.False(t, cfg.Lint.Enabled)
			childConfig := filepath.Join(child, ".bloblangrc.json")
			require.NoError(t, os.WriteFile(childConfig, []byte(`{"formatter":{"printWidth":40}}`), 0600))
			cfg, err = source.Config()
			require.NoError(t, err)
			require.Equal(t, 40, cfg.Formatter.PrintWidth)
			require.True(t, cfg.Lint.Enabled, "nearest settings replace rather than merge")
			path, err := source.ConfigPath()
			require.NoError(t, err)
			require.Equal(t, childConfig, path)
			require.NoError(t, os.WriteFile(childConfig, []byte(`{"formatter":{"printWidth":1}}`), 0600))
			cfg, err = source.Config()
			require.ErrorContains(t, err, childConfig)
			require.Equal(t, Default(), cfg, "invalid nearest config must not fall back to ancestor")
		})
	}
}

func TestSourceExplicitWorkspaceAndStandalone(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	child := filepath.Join(root, "nested")
	dir := filepath.Join(child, "source")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".bloblangrc.json"), []byte(`{"formatter":{"printWidth":40}}`), 0600))
	explicit := ResolveSource(dir, []string{root, child, root + "-unrelated"})
	require.Equal(t, child, explicit.Root)
	cfg, err := explicit.Config()
	require.NoError(t, err)
	require.Equal(t, Default(), cfg, "deepest explicit workspace sets the boundary")
	explicit = ResolveSource(dir, []string{parent})
	require.Equal(t, parent, explicit.Root, "explicit root takes precedence over nested .git")
	loose := filepath.Join(parent, "loose", "nested")
	require.NoError(t, os.MkdirAll(loose, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(parent, ".bloblangrc.json"), []byte(`{"formatter":{"printWidth":120}}`), 0600))
	source := ResolveSource(loose, []string{root})
	require.Empty(t, source.Root, "unrelated workspace must not supply defaults or paths")
	cfg, err = source.Config()
	require.NoError(t, err)
	require.Equal(t, 120, cfg.Formatter.PrintWidth, "standalone lookup without Git reaches ancestors")
}

func TestSourceImportAndExternalMappingPaths(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "nested")
	require.NoError(t, os.Mkdir(dir, 0700))
	rootMapping := filepath.Join(root, "shared.blobl")
	localMapping := filepath.Join(dir, "shared.blobl")
	require.NoError(t, os.WriteFile(rootMapping, []byte("root = this"), 0600))
	source := SourceContext{Dir: dir, Root: root}
	require.Equal(t, rootMapping, source.MappingPath("shared.blobl"))
	require.Equal(t, localMapping, source.ImportPath("shared.blobl"), "imports do not use project fallback")
	require.Equal(t, rootMapping, source.ImportPath(rootMapping))
	require.Equal(t, rootMapping, source.MappingPath(rootMapping))
	require.NoError(t, os.WriteFile(localMapping, []byte("root = this"), 0600))
	require.Equal(t, localMapping, source.MappingPath("shared.blobl"), "source directory takes precedence")
	require.Equal(t, filepath.Join(dir, "missing.blobl"), source.MappingPath("missing.blobl"))
}

func TestSourceWindowsWorkspaceBoundaryCase(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows paths compare workspace roots without case")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "Project")
	dir := filepath.Join(root, "Nested")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(parent, ".bloblangrc.json"), []byte(`{"formatter":{"printWidth":40}}`), 0600))
	source := ResolveSource(strings.ToLower(dir), []string{strings.ToUpper(root)})
	require.Equal(t, strings.ToUpper(root), source.Root)
	cfg, err := source.Config()
	require.NoError(t, err)
	require.Equal(t, Default(), cfg, "case differences must not escape workspace boundary")
}
