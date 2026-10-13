//go:build unix

package link

import (
	"fmt"
	"os"
	"syscall"
)

// prepareSocketDir makes the socket's folder (private to this user) and refuses one that someone else
// made first: a short socket path lives in /tmp, which every user can write to.
func prepareSocketDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a folder (a link or a file is in the way); remove it and start the link again", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user, so the link will not put its socket there; set HANDLOOM_HOME to a shorter folder you own", dir)
	}
	return nil
}
