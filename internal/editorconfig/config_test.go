package editorconfig

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigValidationAndDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".bloblangrc.json")
	c, e := Load(p)
	require.NoError(t, e)
	require.Equal(t, 80, c.Formatter.PrintWidth)
	require.Equal(t, "hint", c.Setting("style/objects/prefer-with").Severity)
	require.True(t, c.Files.RespectGitignore)
	require.Empty(t, c.Files.Exclude)
	for _, raw := range []string{`{"lint":{"rules":{"unknown":"warn"}}}`, `{"formatter":{"printWidth":1}}`, `{"preview":{"format":"xml"}}`, `{"lint":{"rules":{"style/objects/prefer-with":{"severity":"warn","minAssignments":3}}}}`, `{"formatter":{"typo":80}}`} {
		require.NoError(t, os.WriteFile(p, []byte(raw), 0600))
		c, e = Load(p)
		require.Error(t, e, raw)
		require.Equal(t, Default(), c)
	}
	require.NoError(t, os.WriteFile(p, []byte(`{"lint":{"rules":{"style/assignments/prefer-grouped":{"severity":"hint","minAssignments":5}}}}`), 0600))
	c, e = Load(p)
	require.NoError(t, e)
	require.Equal(t, 5, c.Setting("style/assignments/prefer-grouped").MinAssignments)
}

func TestFileSelectionSettings(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".bloblangrc.json")
	require.NoError(t, os.WriteFile(p, []byte(`{"files":{"exclude":["generated/**","!generated/keep.blobl"],"respectGitignore":false}}`), 0600))
	c, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, []string{"generated/**", "!generated/keep.blobl"}, c.Files.Exclude)
	require.False(t, c.Files.RespectGitignore)
	for _, raw := range []string{`{"files":{"exclude":[""]}}`, `{"files":{"exclude":[" "]}}`, `{"files":{"exclude":[42]}}`, `{"files":{"respectGitignore":"no"}}`, `{"files":{"unknown":true}}`} {
		require.NoError(t, os.WriteFile(p, []byte(raw), 0600))
		c, err = Load(p)
		require.Error(t, err, raw)
		require.Equal(t, Default(), c)
	}
}
