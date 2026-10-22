//go:build !unix

package link

import (
	"errors"
	"os"
)

// The link does not run outside Unix; these only let the package build there.
const oNoFollow = 0

func hasOtherNames(fi os.FileInfo) bool { return false }

func execSelf(exe string) error { return errors.New("restarting in place is not supported here") }
