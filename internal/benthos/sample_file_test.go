package benthos

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSampleFileRejectsDirectoriesDevicesAndOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadSampleFile(dir)
	require.ErrorContains(t, err, "regular file")
	_, err = ReadSampleFile(os.DevNull)
	require.Error(t, err, "devices are rejected, including platforms that cannot stat them")
	path := filepath.Join(dir, "large.sample.json")
	file, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(MaxSampleFileBytes+1))
	require.NoError(t, file.Close())
	_, err = ReadSampleFile(path)
	require.ErrorContains(t, err, "limit")
	// Sibling discovery and explicit sample/root/meta directives all use the
	// bounded reader, rather than bypassing it on individual fixture paths.
	_, err = ExtractSample(nil, "file:///large.blobl", "root = this", dir)
	require.ErrorContains(t, err, "limit")
	for _, field := range []string{"sample", "input", "root", "meta"} {
		_, err = ExtractSample(nil, "file:///mapping.blobl", "#!"+field+"_from large.sample.json\nroot = this", dir)
		require.ErrorContains(t, err, "limit")
	}
	valid := []byte(`{"$bloblang":{"input":null,"env":{"NAME":"fixture"}}}`)
	require.NoError(t, os.WriteFile(path, valid, 0600))
	read, err := ReadSampleFile(path)
	require.NoError(t, err)
	require.Equal(t, valid, read)
}
