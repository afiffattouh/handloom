package hub

import (
	"fmt"
	"strings"

	"handloom/internal/api"
)

// scopeRefused records that a device refused an agent's submit: the agent had
// changed files outside what its profile allows. The check itself runs on the
// device that has the files; the hub keeps the record and tells the lead.
func scopeRefused(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a device reports a refused submit")
	}
	var req api.ScopeRefusal
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	a, err := c.agentByName(req.Agent)
	if err != nil {
		return nil, err
	}
	if a == nil || a.deviceID != c.p.deviceID {
		return nil, forbidden("agent %q is not on this device", req.Agent)
	}
	paths := req.Paths
	if len(paths) > 20 {
		paths = append(paths[:20:20], fmt.Sprintf("... and %d more", len(req.Paths)-20))
	}
	for i, p := range paths {
		if len(p) > 200 {
			paths[i] = p[:200]
		}
	}
	t, err := c.task(req.Task)
	if err != nil {
		return nil, err
	}
	if t.projectID != a.projectID {
		return nil, notFound("no task %d", req.Task)
	}
	if err := c.record(a.projectID, a.id, "task.scope_refused", t.target(), map[string]any{"agent": a.name, "paths": paths}); err != nil {
		return nil, err
	}
	lead, err := c.leadOf(a)
	if err != nil {
		return nil, err
	}
	body := fmt.Sprintf("%s tried to submit task #%d (%s) but had changed files its profile does not allow: %s. The submit was refused; it has to revert them.",
		a.name, t.id, t.title, strings.Join(paths, ", "))
	if lead != nil && lead.id != a.id {
		return nil, c.hubMessage(lead, &t.id, body)
	}
	return nil, nil
}
