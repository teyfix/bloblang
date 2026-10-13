//go:build !windows

package cli

import (
	"errors"
	"os"
)

func globLookupMiss(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
