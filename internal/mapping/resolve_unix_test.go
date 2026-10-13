//go:build unix

package mapping

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadRequestFileRejectsFIFOWithoutOpening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocked.fifo")
	require.NoError(t, syscall.Mkfifo(path, 0o600))
	done := make(chan error, 1)
	go func() {
		_, err := readRequestFile(path)
		done <- err
	}()
	select {
	case err := <-done:
		require.ErrorContains(t, err, "not a regular file")
	case <-time.After(time.Second):
		t.Fatal("reading a FIFO blocked instead of rejecting it")
	}
}
