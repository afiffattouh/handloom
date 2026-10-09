//go:build !unix

package link

import "os/exec"

// The link does not run outside Unix, so there is no process group to manage.
func inOwnGroup(cmd *exec.Cmd) {}
