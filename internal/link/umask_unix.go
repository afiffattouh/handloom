//go:build unix

package link

import "syscall"

func umask(mask int) int { return syscall.Umask(mask) }
