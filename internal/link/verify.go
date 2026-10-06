package link

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
)

const maxVerifyLog = 256 << 10

// runVerify runs the job's verify command in the agent's worktree and reports
// the result to the hub: exit code, a timeout flag, the end of the output and
// the hash of the whole log (which stays on this device). The command is
// written by a human and stored on the hub, like a profile: this device
// trusts the hub for it.
func (l *Link) runVerify(rec *scopeRecord, taskID int64) {
	res := l.execVerify(rec.Dir, rec.Verify, fmt.Sprintf("%s-%d", rec.Agent, taskID))
	req := api.TaskCheckReq{Agent: rec.Agent, Command: rec.Verify, ExitCode: res.exit, TimedOut: res.timedOut, Tail: res.tail, SHA256: res.sha}
	if err := l.hub.Do(context.Background(), "POST", fmt.Sprintf("/v1/tasks/%d/verify", taskID), req, nil); err != nil {
		l.opt.Log.Printf("report verification of task %d: %v", taskID, err)
		return
	}
	l.opt.Log.Printf("verified task %d (%s): exit %d, timed out %v, log %s", taskID, rec.Agent, res.exit, res.timedOut, res.logPath)
}

type verifyResult struct {
	exit               int
	timedOut           bool
	tail, sha, logPath string
}

// execVerify runs a verify command in dir and keeps its whole log on this device.
func (l *Link) execVerify(dir, command, logName string) verifyResult {
	ctx, cancel := context.WithTimeout(context.Background(), l.opt.VerifyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = verifyEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // so a timeout stops the whole tree
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	cmd.Stdout = &limited{w: &out, left: maxVerifyLog}
	cmd.Stderr = cmd.Stdout
	err := cmd.Run()
	exit, timedOut := 0, ctx.Err() == context.DeadlineExceeded
	if err != nil {
		exit = -1
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		}
	}
	sum := sha256.Sum256(out.Bytes())
	logPath := filepath.Join(client.Home(), "verify", fmt.Sprintf("%s-%d.log", logName, time.Now().Unix()))
	if os.MkdirAll(filepath.Dir(logPath), 0o700) == nil {
		os.WriteFile(logPath, out.Bytes(), 0o600)
	}
	tail := out.String()
	if len(tail) > 3000 {
		tail = tail[len(tail)-3000:]
	}
	return verifyResult{exit: exit, timedOut: timedOut, tail: strings.TrimSpace(tail), sha: hex.EncodeToString(sum[:]), logPath: logPath}
}

// verifyEnv is a plain environment: the link's own variables (tokens, hub
// addresses) are not for the command.
func verifyEnv() []string {
	env := []string{"CI=1", "HANDLOOM_VERIFY=1"}
	for _, k := range []string{"PATH", "HOME", "USER", "LANG", "LC_ALL", "TMPDIR", "GOPATH", "GOCACHE", "GOFLAGS"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// limited stops collecting after left bytes but keeps accepting writes, so a
// noisy command is not slowed down by a full buffer.
type limited struct {
	w    *bytes.Buffer
	left int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.left > 0 {
		n := len(p)
		if n > l.left {
			n = l.left
		}
		l.w.Write(p[:n])
		l.left -= n
	}
	return len(p), nil
}
