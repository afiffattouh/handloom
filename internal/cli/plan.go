package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"handloom/internal/plan"
)

func (e *env) jobPlan(args []string) error {
	fs := e.flags("job plan")
	file := fs.String("file", "", "submit a version-1 JSON plan; omit to show its status")
	pos, err := fs.need(args, 1, 1, "job plan <job> [--file plan.json]")
	if err != nil {
		return err
	}
	id, err := strconv.ParseInt(pos[0], 10, 64)
	if err != nil || id <= 0 {
		return usageErr("job id must be a positive integer")
	}
	var req plan.Plan
	if *file != "" {
		f, err := os.Open(*file)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
		f.Close()
		if err != nil {
			return err
		}
		if len(b) > 1<<20 {
			return usageErr("plan file exceeds 1 MiB")
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			return fmt.Errorf("read plan: %w", err)
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			return usageErr("plan file must contain one JSON object")
		}
		if req.MaxAttempts == 0 {
			req.MaxAttempts = 2
		}
		if _, err := req.Order(); err != nil {
			return err
		}
	}
	c, err := e.conn()
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/v1/jobs/%d/plan", id)
	var status plan.Status
	if *file != "" {
		err = c.Post(path, req, &status)
	} else {
		err = c.Get(path, &status)
	}
	if err != nil {
		return err
	}
	e.print(status, func() {
		fmt.Fprintf(e.out, "Plan for job #%d (up to %d attempts per step)\n", id, status.MaxAttempts)
		for _, s := range status.Steps {
			fmt.Fprintf(e.out, "%s: task #%d, %s, %s, attempts %d", s.Key, s.Task, s.Status, s.Profile, s.Attempts)
			if s.Worker != "" {
				fmt.Fprintf(e.out, ", worker %s", s.Worker)
			}
			if s.Detail != "" {
				fmt.Fprintf(e.out, " — %s", s.Detail)
			}
			fmt.Fprintln(e.out)
		}
	})
	return nil
}
