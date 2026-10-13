//go:build unix

package benthos

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSampleFileRejectsFIFOWithoutOpening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.json")
	require.NoError(t, syscall.Mkfifo(path, 0600))
	_, err := ReadSampleFile(path)
	require.ErrorContains(t, err, "regular file")
}
