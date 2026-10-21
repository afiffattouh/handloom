package hub

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"handloom/internal/addon"
	"handloom/internal/api"
	"handloom/internal/store"
)

// Grants, the tool snapshot a spawn gets, and the call journal a link reports (docs/direction/addons.md).
// The hub holds the owner's grants and says, when it starts a spawn, which tools that spawn may call on its
// computer. The link decides each call from that snapshot; the hub is not on the per-call path.

const (
	defaultMaxCalls = 50
	maxMaxCalls     = 10000
	maxCallBatch    = 200
	maxCallMillis   = int64(24 * time.Hour / time.Millisecond)
	maxCallSize     = int64(50 << 20) // the largest per-call output cap a manifest may ask for
)

var (
	toolNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	callIDRe   = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
)

// toolCodes are the result codes of the gateway, fixed by the design. A journal line carries one of these
// and never free text.
var toolCodes = map[string]bool{
	"ok": true, "not-installed": true, "denied-grant": true, "denied-confidential": true, "denied-budget": true,
	"changed": true, "plugin-down": true, "plugin-error": true, "timeout": true,
}

// callPhases are the three kinds of journal line. A call line is written before the add-on is asked.
var callPhases = map[string]bool{"call": true, "result": true, "denied": true}

// addonCallAction is the audit name of a journal line: the add-on's own prefix, never a built-in name. The
// built-in add-on actions have two parts (addon.grant, addon.revoke); this has three, and both the name and
// the phase were checked against fixed shapes before it is made.
func addonCallAction(name, phase string) string {
	return "addon." + name + "." + phase
}

// ---- grants ----

func grantFrom(s scanner) (api.AddonGrant, error) {
	var g api.AddonGrant
	var leaves int
	var at int64
	err := s.Scan(&g.Profile, &g.Addon, &g.Tool, &g.ManifestHash, &leaves, &g.MaxCalls, &g.GrantedBy, &at)
	g.LeavesMachine = leaves != 0
	g.GrantedAt = store.Time(at)
	return g, err
}

const grantSelect = `SELECT profile, addon, tool, manifest_hash, leaves_machine, max_calls, granted_by, granted_at FROM addon_grant `

func (c *call) grants(profile string) ([]api.AddonGrant, error) {
	q, args := grantSelect, []any{}
	if profile != "" {
		q += `WHERE profile = ? `
		args = append(args, profile)
	}
	rows, err := c.tx.Query(q+`ORDER BY profile, addon, tool`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.AddonGrant{}
	for rows.Next() {
		g, err := grantFrom(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// grantTarget finds the manifest a grant is for: the one an approved copy of the add-on reported, with the
// tool in it. Computers may hold different versions; then the owner names the computer.
func (c *call) grantTarget(addonName, tool, computer string) (string, error) {
	q := `SELECT d.name, a.manifest_hash, a.manifest, a.status FROM addon a JOIN device d ON d.id = a.device_id
		WHERE d.revoked_at IS NULL AND a.name = ?`
	args := []any{addonName}
	if computer != "" {
		q += ` AND d.name = ?`
		args = append(args, computer)
	}
	rows, err := c.tx.Query(q+` ORDER BY d.name`, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var known, approved int
	var tools []string
	hashes := map[string]bool{}
	for rows.Next() {
		var dev, hash, text, status string
		if err := rows.Scan(&dev, &hash, &text, &status); err != nil {
			return "", err
		}
		known++
		// Only a manifest that was approved on its computer can be granted: a changed one has not been.
		if (status != "ready" && status != "down") || text == "" || hash == "" {
			continue
		}
		m, err := addon.Parse([]byte(text))
		if err != nil {
			continue
		}
		approved++
		if _, ok := m.Tool(tool); ok {
			hashes[hash] = true
		} else {
			for _, t := range m.Tools {
				tools = append(tools, t.Name)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch {
	case known == 0 && computer != "":
		return "", notFound("the computer %s has not reported an add-on called %s", computer, addonName)
	case known == 0:
		return "", notFound("no computer has reported an add-on called %s; install it on a computer first", addonName)
	case approved == 0:
		return "", conflict("%s is on a computer but is not approved there as it is now, or its description was not reported; approve it on the computer, then grant again", addonName)
	case len(hashes) == 0:
		sort.Strings(tools)
		return "", badRequest("the add-on %s has no tool called %s; its tools are: %s", addonName, tool, strings.Join(uniq(tools), ", "))
	case len(hashes) > 1:
		return "", conflict("computers have different versions of %s; name the computer whose version this grant is for", addonName)
	}
	for h := range hashes {
		return h, nil
	}
	return "", nil
}

func uniq(in []string) []string {
	var out []string
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// addonGrantNew lets a profile use one tool of an add-on. Granting again replaces the grant, which is how
// a grant is renewed after the add-on's manifest changed.
func addonGrantNew(c *call) (any, error) {
	if err := c.allow(ActAddonManage); err != nil {
		return nil, err
	}
	var req api.AddonGrantReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	name := c.r.PathValue("name")
	if _, err := c.profileVersion(name, 0); err != nil {
		return nil, err
	}
	req.Addon = strings.ToLower(req.Addon)
	if !addonNameRe.MatchString(req.Addon) {
		return nil, badRequest("%q is not an add-on name", req.Addon)
	}
	if !toolNameRe.MatchString(req.Tool) {
		return nil, badRequest("%q is not a tool name", req.Tool)
	}
	if req.MaxCalls < 0 || req.MaxCalls > maxMaxCalls {
		return nil, badRequest("the call limit is between 1 and %d (leave it out for %d)", maxMaxCalls, defaultMaxCalls)
	}
	if req.MaxCalls == 0 {
		req.MaxCalls = defaultMaxCalls
	}
	leaves := true
	if req.LeavesMachine != nil {
		leaves = *req.LeavesMachine
	}
	hash, err := c.grantTarget(req.Addon, req.Tool, req.Computer)
	if err != nil {
		return nil, err
	}
	l := 0
	if leaves {
		l = 1
	}
	if _, err := c.tx.Exec(`INSERT INTO addon_grant(profile, addon, tool, manifest_hash, leaves_machine, max_calls, granted_by, granted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(profile, addon, tool) DO UPDATE SET manifest_hash = excluded.manifest_hash, leaves_machine = excluded.leaves_machine,
			max_calls = excluded.max_calls, granted_by = excluded.granted_by, granted_at = excluded.granted_at`,
		name, req.Addon, req.Tool, hash, l, req.MaxCalls, c.p.actor(), store.Millis(c.now)); err != nil {
		return nil, err
	}
	if err := c.audit("addon.grant", "profile:"+name, map[string]any{"addon": req.Addon, "tool": req.Tool,
		"manifest": hash, "leaves_machine": leaves, "max_calls": req.MaxCalls}); err != nil {
		return nil, err
	}
	return grantFrom(c.tx.QueryRow(grantSelect+`WHERE profile = ? AND addon = ? AND tool = ?`, name, req.Addon, req.Tool))
}

func addonGrantRevoke(c *call) (any, error) {
	if err := c.allow(ActAddonManage); err != nil {
		return nil, err
	}
	name, addonName, tool := c.r.PathValue("name"), strings.ToLower(c.r.PathValue("addon")), c.r.PathValue("tool")
	res, err := c.tx.Exec(`DELETE FROM addon_grant WHERE profile = ? AND addon = ? AND tool = ?`, name, addonName, tool)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, notFound("the profile %s has no grant for %s.%s", name, addonName, tool)
	}
	if err := c.audit("addon.revoke", "profile:"+name, map[string]any{"addon": addonName, "tool": tool}); err != nil {
		return nil, err
	}
	return nil, nil
}

// addonGrantList is every grant, or one profile's with ?profile=.
func addonGrantList(c *call) (any, error) {
	if err := c.allow(ActAddonManage); err != nil {
		return nil, err
	}
	return c.grants(c.r.URL.Query().Get("profile"))
}

// ---- the snapshot ----

// toolWhy and toolAction are the fixed words for each cause: what it means, and the one thing to do.
var toolWhy = map[string]string{
	api.CauseNotInstalled:       "This computer does not have the add-on.",
	api.CauseChanged:            "The add-on is not the version the tool was granted for.",
	api.CausePluginDown:         "The add-on is not running and could not be started.",
	api.CauseDeniedConfidential: "A confidential job may not use a tool that leaves the computer.",
}

const (
	actionInstall      = "Install the add-on on this computer."
	actionApprove      = "Approve the changed add-on again on this computer, then grant the tool again if the tool list changed."
	actionGrantAgain   = "Check the new version of the add-on, then grant the tool again on the profile."
	actionDown         = "Run handloom addon doctor on this computer."
	actionConfidential = "Use a tool that stays on the computer, or a different job."
)

type reportedAddon struct {
	hash, status string
	manifest     *addon.Manifest
}

// deviceAddons is what a computer last reported, with each manifest read once.
func (c *call) deviceAddons(deviceID int64) (map[string]reportedAddon, error) {
	rows, err := c.tx.Query(`SELECT name, manifest_hash, manifest, status FROM addon WHERE device_id = ?`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]reportedAddon{}
	for rows.Next() {
		var name, hash, text, status string
		if err := rows.Scan(&name, &hash, &text, &status); err != nil {
			return nil, err
		}
		r := reportedAddon{hash: hash, status: status}
		if text != "" {
			if m, err := addon.Parse([]byte(text)); err == nil {
				r.manifest = m
			}
		}
		out[name] = r
	}
	return out, rows.Err()
}

// toolSnapshot works out, for one profile on one computer, the tools a spawn may call and the tools it is
// granted but cannot have. A tool is available only where its grant was made for the manifest installed on
// that computer and the add-on is ready; a confidential job never gets a tool that leaves the computer.
// The causes are checked in this order: not installed, changed, confidential, down.
func (c *call) toolSnapshot(profileName string, deviceID int64, confidential bool) ([]api.SpawnTool, []api.ToolUnavailable, error) {
	if profileName == "" {
		return nil, nil, nil
	}
	grants, err := c.grants(profileName)
	if err != nil || len(grants) == 0 {
		return nil, nil, err
	}
	have, err := c.deviceAddons(deviceID)
	if err != nil {
		return nil, nil, err
	}
	var tools []api.SpawnTool
	var out []api.ToolUnavailable
	no := func(g api.AddonGrant, cause, action string) {
		out = append(out, api.ToolUnavailable{Addon: g.Addon, Tool: g.Tool, Cause: cause, Why: toolWhy[cause], Action: action})
	}
	for _, g := range grants {
		a, ok := have[g.Addon]
		switch {
		case !ok || a.status == "missing":
			no(g, api.CauseNotInstalled, actionInstall)
			continue
		case a.status == "changed":
			no(g, api.CauseChanged, actionApprove)
			continue
		case a.hash != g.ManifestHash || a.manifest == nil:
			no(g, api.CauseChanged, actionGrantAgain)
			continue
		}
		t, ok := a.manifest.Tool(g.Tool)
		if !ok {
			no(g, api.CauseChanged, actionGrantAgain)
			continue
		}
		if confidential && g.LeavesMachine {
			no(g, api.CauseDeniedConfidential, actionConfidential)
			continue
		}
		if a.status != "ready" {
			no(g, api.CausePluginDown, actionDown)
			continue
		}
		st := api.SpawnTool{Addon: g.Addon, Tool: g.Tool, Summary: t.Summary, LeavesMachine: g.LeavesMachine,
			MaxCalls: g.MaxCalls, TimeoutSeconds: int(t.Timeout / time.Second), MaxOutput: t.MaxOutput}
		names := make([]string, 0, len(t.Input))
		for n := range t.Input {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			f := t.Input[n]
			st.Input = append(st.Input, api.ToolField{Name: n, Type: f.Type, Required: f.Required, Summary: f.Summary})
		}
		tools = append(tools, st)
	}
	return tools, out, nil
}

// jobToolsUnavailable is, for a job, the tools its helpers' profiles are granted but could not have, with
// the cause and the one action. The job page uses it for its notice. Spawns that failed to start are left
// out: the notice is about helpers that were started without a tool they should have had.
func (c *call) jobToolsUnavailable(jobID int64) ([]api.ToolUnavailable, error) {
	j, err := c.task(jobID)
	if err != nil {
		return nil, err
	}
	rows, err := c.spawns(`WHERE s.job_id = ? AND s.status != 'failed' AND s.profile_name != ''`, jobID)
	if err != nil {
		return nil, err
	}
	out := []api.ToolUnavailable{}
	seen := map[string]bool{}
	for _, s := range rows {
		_, un, err := c.toolSnapshot(s.profileName, s.deviceID, j.confidential)
		if err != nil {
			return nil, err
		}
		for _, u := range un {
			if k := u.Addon + "." + u.Tool + "/" + u.Cause; !seen[k] {
				seen[k] = true
				out = append(out, u)
			}
		}
	}
	return out, nil
}

// ---- the call journal ----

// addonCallsReport stores a link's batch of journal lines as audit entries under each add-on's own prefix.
// A line holds ids, names, a code, a size and a time: there is no field for query text or results, and a
// request that carries any field it does not know is refused whole. A line already stored (same call and
// phase from the same computer) is skipped, so a batch sent twice counts once.
func addonCallsReport(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a computer's link reports add-on calls")
	}
	var req api.AddonCallReport
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if len(req.Calls) > maxCallBatch {
		return nil, badRequest("too many calls in one report; at most %d", maxCallBatch)
	}
	spawns := map[int64]*spawnRow{}
	for i, e := range req.Calls {
		s, ok := spawns[e.Spawn]
		if !ok {
			var err error
			if s, err = scanSpawn(c.tx.QueryRow(spawnSelect+`WHERE s.id = ?`, e.Spawn)); err != nil {
				return nil, err
			}
			spawns[e.Spawn] = s
		}
		if err := c.checkAddonCall(i, e, s); err != nil {
			return nil, err
		}
	}
	for _, e := range req.Calls {
		res, err := c.tx.Exec(`INSERT OR IGNORE INTO addon_call_seen(device_id, call_id, phase) VALUES (?, ?, ?)`, c.p.deviceID, e.CallID, e.Phase)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		s := spawns[e.Spawn]
		target := fmt.Sprintf("spawn:%d", s.id)
		if s.job.Valid {
			target = fmt.Sprintf("job:%d", s.job.Int64)
		}
		if err := c.audit(addonCallAction(e.Addon, e.Phase), target, map[string]any{"call": e.CallID, "spawn": s.id, "agent": e.Agent,
			"job": e.Job, "tool": e.Tool, "code": e.Code, "size": e.Size, "millis": e.Millis, "at": e.Time.UTC().Format(time.RFC3339)}); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (c *call) checkAddonCall(i int, e api.AddonCall, s *spawnRow) error {
	n := i + 1
	switch {
	case !callIDRe.MatchString(e.CallID):
		return badRequest("call %d: the call id must be 1 to 64 letters, digits, '.', '_', ':' or '-'", n)
	case s == nil:
		return badRequest("call %d: there is no spawn %d", n, e.Spawn)
	case s.deviceID != c.p.deviceID:
		return forbidden("call %d: spawn %d belongs to another computer", n, e.Spawn)
	case e.Agent != s.name:
		return badRequest("call %d: spawn %d is not the agent %q", n, e.Spawn, e.Agent)
	case e.Job != s.job.Int64:
		return badRequest("call %d: the job does not match spawn %d", n, e.Spawn)
	case !addonNameRe.MatchString(e.Addon):
		return badRequest("call %d: %q is not an add-on name", n, e.Addon)
	case !toolNameRe.MatchString(e.Tool):
		return badRequest("call %d: %q is not a tool name", n, e.Tool)
	case !callPhases[e.Phase]:
		return badRequest("call %d: the phase must be call, result or denied", n)
	case e.Phase == "call" && e.Code != "":
		return badRequest("call %d: a call line has no result code", n)
	case e.Phase != "call" && !toolCodes[e.Code]:
		return badRequest("call %d: %q is not one of the result codes", n, e.Code)
	case e.Phase == "denied" && e.Code == "ok":
		return badRequest("call %d: a denied call cannot be ok", n)
	case e.Size < 0 || e.Size > maxCallSize:
		return badRequest("call %d: the size is out of range", n)
	case e.Millis < 0 || e.Millis > maxCallMillis:
		return badRequest("call %d: the time taken is out of range", n)
	case e.Time.IsZero() || e.Time.Year() < 2020 || e.Time.After(c.now.Add(24*time.Hour)):
		return badRequest("call %d: the time is missing or not believable", n)
	}
	return nil
}
