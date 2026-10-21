//go:build !unix

package addon

import "os/exec"

// The link does not run outside Unix, so there is no process group to manage.
func ownGroup(cmd *exec.Cmd) {}

func killTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}

func alive(pid int) bool { return false }
