//go:build unix

package addon

import (
	"os"
	"os/exec"
	"syscall"
)

// ownGroup puts the service in its own process group, so stopping it stops anything it started.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// lockPid takes a lock on the file that lasts until it is closed, or the process ends.
func lockPid(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }

// pidHeld says whether some process holds the lock on the file, that is, whether the service is really alive.
func pidHeld(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) == nil {
		return false
	}
	return true
}
