package link

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"handloom/internal/api"
)

const (
	maxResultDiff  = 180 << 10
	maxNewFile     = 24 << 10
	maxResultPages = 8
	maxPageSize    = 64 << 10
	resultFileCap  = 200
)

// reportResult tells the hub what a submitted task changed: the files, the diff against the commit the
// worktree started from, and the web pages among them. It is for a person to read; nothing is decided by it.
func (l *Link) reportResult(rec *scopeRecord, taskID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, err := l.collectResult(ctx, rec)
	if err != nil {
		l.opt.Log.Printf("collect the result of task %d: %v", taskID, err)
		return
	}
	if err := l.hub.Do(ctx, "POST", fmt.Sprintf("/v1/tasks/%d/result", taskID), req, nil); err != nil {
		l.opt.Log.Printf("report the result of task %d: %v", taskID, err)
	}
}

func (l *Link) collectResult(ctx context.Context, rec *scopeRecord) (api.TaskResultReq, error) {
	req := api.TaskResultReq{Agent: rec.Agent, Pages: map[string]string{}}
	git := func(args ...string) (string, error) {
		return l.opt.Git(ctx, "git", append([]string{"-C", rec.Dir}, args...)...)
	}
	changed, err := l.changedFiles(ctx, rec)
	if err != nil {
		return req, err
	}
	tracked, err := git("diff", "--name-only", "--no-renames", "-z", rec.Base)
	if err != nil {
		return req, err
	}
	isTracked := map[string]bool{}
	for _, p := range strings.Split(tracked, "\x00") {
		if p != "" {
			isTracked[p] = true
		}
	}
	var diff strings.Builder
	for i, p := range changed {
		if i >= resultFileCap {
			req.Truncated = true
			break
		}
		f := api.ResultFile{Path: p, Status: "modified"}
		full := filepath.Join(rec.Dir, p)
		_, statErr := os.Stat(full)
		gone := statErr != nil
		if isTracked[p] {
			if out, err := git("diff", "--numstat", rec.Base, "--", p); err == nil {
				fmt.Sscanf(strings.TrimSpace(out), "%d\t%d", &f.Added, &f.Deleted)
			}
			if gone {
				f.Status = "deleted"
			} else if _, err := git("cat-file", "-e", rec.Base+":"+p); err != nil {
				f.Status = "added"
			}
			if out, err := git("diff", "--no-color", "-U3", rec.Base, "--", p); err == nil && diff.Len() < maxResultDiff {
				diff.WriteString(out)
				if !strings.HasSuffix(out, "\n") {
					diff.WriteString("\n")
				}
			}
		} else if !gone { // a new file git has not seen
			f.Status = "added"
			b, err := os.ReadFile(full)
			if err == nil && !strings.ContainsRune(string(b), 0) {
				f.Added = strings.Count(string(b), "\n")
				if len(b) > 0 && b[len(b)-1] != '\n' {
					f.Added++
				}
				if diff.Len() < maxResultDiff {
					text := string(b)
					if len(text) > maxNewFile {
						text = text[:maxNewFile] + "\n... (cut)\n"
						req.Truncated = true
					}
					fmt.Fprintf(&diff, "diff --git a/%s b/%s\nnew file\n--- /dev/null\n+++ b/%s\n", p, p, p)
					for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
						diff.WriteString("+" + line + "\n")
					}
				}
			}
		}
		req.Files = append(req.Files, f)
		if !gone && len(req.Pages) < maxResultPages {
			if lp := strings.ToLower(p); strings.HasSuffix(lp, ".html") || strings.HasSuffix(lp, ".htm") {
				if st, err := os.Stat(full); err == nil && st.Size() <= maxPageSize {
					if b, err := os.ReadFile(full); err == nil {
						req.Pages[p] = string(b)
					}
				}
			}
		}
	}
	req.Diff = diff.String()
	if len(req.Diff) > maxResultDiff {
		req.Diff, req.Truncated = req.Diff[:maxResultDiff], true
	}
	return req, nil
}
