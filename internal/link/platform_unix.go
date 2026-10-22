//go:build unix

package link

import (
	"os"
	"syscall"
)

// oNoFollow makes an open fail on a symbolic link.
const oNoFollow = syscall.O_NOFOLLOW

// hasOtherNames says whether a regular file has more than one name: a hard link to a file elsewhere would
// show that file's content as a result.
func hasOtherNames(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}

// execSelf starts exe in place of this process, with the same arguments.
func execSelf(exe string) error { return syscall.Exec(exe, os.Args, os.Environ()) }
