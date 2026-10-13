package cli

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWindowsWildcardLiteralProbeFallsBackToGlob(t *testing.T) {
	dir := ignoreFixture(t)
	path := filepath.Join(dir, "nested folder", "mapping.blobl")
	writeInput(t, path, "root = this\n")
	for _, pattern := range []string{
		"**/*.blobl",
		filepath.Join("**", "*.blobl"),
		filepath.Join(dir, "**", "*.blobl"),
	} {
		code, out, stderr := invoke(t, "", "lint", "--format", "json", pattern)
		require.Zero(t, code, stderr)
		require.Equal(t, "[]\n", out)
		code, out, stderr = invoke(t, "", "fmt", pattern)
		require.Zero(t, code, stderr)
		require.Equal(t, "root = this\n", out)
	}
}
