//go:build !unix

package link

// There is no umask outside Unix; the link itself only runs on Linux and macOS.
func umask(mask int) int { return 0 }
