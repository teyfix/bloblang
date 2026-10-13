package cli

import (
	"errors"
	"os"
	"syscall"
)

func globLookupMiss(err error) bool {
	// Windows rejects wildcard filenames during the literal probe rather than
	// reporting a missing file. Let doublestar interpret those arguments.
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ERROR_INVALID_NAME)
}
