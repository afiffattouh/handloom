//go:build !unix

package link

import "os"

func prepareSocketDir(dir string) error { return os.MkdirAll(dir, 0o700) }
