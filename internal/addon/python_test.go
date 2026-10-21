package addon

import "os/exec"

func lookPython() (string, error) { return exec.LookPath("python3") }
