package cli

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func globLookupMiss(err error) bool {
	// Windows rejects wildcard filenames during the literal probe rather than
	// reporting a missing file. Let doublestar interpret those arguments.
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, windows.ERROR_INVALID_NAME)
}
