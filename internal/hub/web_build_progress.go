package hub

import (
	"fmt"
	"strings"

	"handloom/internal/api"
	"handloom/internal/playbook"
)

type buildStageView struct {
	Task                        int64
	Title, State, Badge, Detail string
	Attempts                    int
}

type buildProgressView struct {
	Stages             []buildStageView
	Accepted, Total    int
	State, Description string
	Complete           bool
}

func buildProgress(v *jobView2) *buildProgressView {
	b := &buildProgressView{Total: len(v.Plan.Steps), State: "Waiting to start", Description: "Stages start when their dependencies and worker capacity are ready. Stage updates appear here."}
	tasks := map[int64]taskView{}
	for _, t := range v.Tasks {
		tasks[t.ID] = t
	}
	var active []string
	attention, integrating := false, false
	for _, s := range v.Plan.Steps {
		t := tasks[s.Task]
		stage := buildStageView{Task: s.Task, Title: s.Title, Attempts: s.Attempts, State: "Waiting", Detail: s.Detail}
		switch s.Status {
		case api.StatusDone:
			b.Accepted++
			stage.State, stage.Badge, stage.Detail = "Accepted", "success", ""
			if t.Merge != nil && t.Merge.Status != "merged" {
				integrating = true
				stage.State, stage.Badge, stage.Detail = "Integrating", "warning", "Accepted work is being merged and checked."
				if t.Merge.Status == "failed" || t.Merge.Status == "conflict" {
					attention = true
					stage.State, stage.Badge, stage.Detail = "Integration blocked", "destructive", t.Merge.Detail
				}
			}
		case api.StatusClaimed:
			stage.State, stage.Badge, stage.Detail = "Working", "info", ""
			active = append(active, s.Title)
		case api.StatusSubmitted:
			stage.State, stage.Badge, stage.Detail = "Under review", "info", "Awaiting review of the evidence and project checks."
			active = append(active, s.Title)
			if t.Check != nil && !t.CheckOK {
				stage.State, stage.Badge, stage.Detail = "Check failed", "warning", "The failed check must be resolved before this stage can be accepted."
			}
		case api.StatusCancelled:
			stage.State, stage.Badge, stage.Detail = "Stopped", "outline", "This stage was cancelled. Dependent stages cannot proceed."
			attention = true
		default:
			if s.Detail != "" && !strings.HasPrefix(s.Detail, "Waiting for") {
				stage.State, stage.Badge = "Needs attention", "warning"
				attention = true
			}
		}
		b.Stages = append(b.Stages, stage)
	}
	b.Complete = b.Total > 0 && b.Accepted == b.Total && !integrating
	switch {
	case v.Job.Status == api.StatusCancelled:
		b.State, b.Description = "Build stopped", "Work already accepted is retained. This build will not start more stages."
		b.Complete = false
	case b.Complete:
		b.State, b.Description = "Release handoff ready", "All stages are accepted and integration is complete. Review the release evidence below. Deployment is not automatic."
	case !v.Open:
		b.State, b.Description = "Build closed with unfinished stages", "Review the stage evidence before using this result. A closed build does not establish release readiness."
	case attention || v.Job.Progress.State == "stuck" || v.Job.Progress.State == "needs_you":
		b.State, b.Description = "Needs attention", "Review the stage details below. Check the Inbox for questions and use technical details to inspect failures."
		if v.Job.Progress.Reason != "" {
			b.Description += " " + v.Job.Progress.Reason
		}
	case len(active) > 0:
		b.State, b.Description = "Build in progress", strings.Join(active, " · ")
	case integrating:
		b.State, b.Description = "Checking integrated work", "Accepted changes must pass integration before dependent stages can start."
	case b.Accepted > 0:
		b.State, b.Description = "Preparing the next stage", fmt.Sprintf("%d stages accepted. Handloom starts the next specialist when its dependencies and worker capacity are ready.", b.Accepted)
	}
	if v.Plan.Playbook != playbook.MVP && b.Complete {
		b.State = "Workflow ready to review"
		b.Description = "All stages are accepted and integration is complete. Review the stage evidence below."
	}
	return b
}
