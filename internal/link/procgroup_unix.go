//go:build unix

package link

import (
	"os/exec"
	"syscall"
)

// inOwnGroup puts the command in its own process group and makes cancelling it
// stop the whole tree, so a verify command that times out leaves nothing behind.
func inOwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
