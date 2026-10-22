package hub

import (
	"database/sql"
	"errors"
	"strings"

	"handloom/internal/api"
	"handloom/internal/store"
)

// Machines belong to people (D108). A machine's owner_id is the person who added it; NULL means
// the admin token made it, or it is older than this rule ("shared by the owner"). An owner (and the
// admin token) may use and see every machine. A member sees and uses only the machines they own.
// A viewer sees and uses none. Agents and devices keep their own paths, which this does not touch.

// humanID is the calling person's id; 0 for anyone who is not a person.
func (c *call) humanID() (int64, error) {
	if c.p.kind != kindHuman {
		return 0, nil
	}
	var id int64
	err := c.tx.QueryRow(`SELECT id FROM human WHERE name = ?`, c.p.name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// isOwnerClass is true for the owner and the admin token: they may use every machine.
func (c *call) isOwnerClass() bool {
	return c.p.kind == kindAdmin || (c.p.kind == kindHuman && c.p.role == store.RoleOwner)
}

// deviceScope is the set of machines this caller may see and use. Devices (agents) and the hub's own
// sweeps are not people: their paths are unchanged, so they get all.
func (c *call) deviceScope() store.DeviceScope {
	switch {
	case c.p.kind != kindHuman:
		return store.DeviceScope{All: true}
	case c.p.role == store.RoleOwner:
		return store.DeviceScope{All: true}
	case c.p.role == store.RoleMember:
		id, err := c.humanID()
		if err != nil {
			return store.DeviceScope{}
		}
		return store.DeviceScope{Owner: id}
	}
	return store.DeviceScope{} // a viewer
}

// ownMachines names the machines the caller owns that can still be used, for the words of a refusal.
func (c *call) ownMachines() []string {
	sc := c.deviceScope()
	if sc.All {
		return nil
	}
	where, args := sc.Clause("d")
	rows, err := c.tx.Query(`SELECT d.name FROM device d WHERE d.revoked_at IS NULL AND d.credential_hash IS NOT NULL AND `+where+` ORDER BY d.name`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			out = append(out, n)
		}
	}
	return out
}

// useWords is the end of a refusal: what the person can do instead.
func (c *call) useWords() string {
	own := c.ownMachines()
	if len(own) == 0 {
		return "You have no machines yet: add one under Machines."
	}
	return "You can use your own machines: " + strings.Join(own, ", ") + ", or add one under Machines."
}

// useDevice says whether the caller may start work on, or ask things of, the machine with this id.
func (c *call) useDevice(id int64) error {
	sc := c.deviceScope()
	if sc.All {
		return nil
	}
	if c.p.kind == kindHuman && c.p.role == store.RoleViewer {
		return forbidden("A viewer cannot use machines.")
	}
	var owner sql.NullInt64
	var ownerName string
	err := c.tx.QueryRow(`SELECT d.owner_id, COALESCE(h.name, '') FROM device d LEFT JOIN human h ON h.id = d.owner_id WHERE d.id = ?`, id).Scan(&owner, &ownerName)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("no such machine")
	}
	if err != nil {
		return err
	}
	if owner.Valid && owner.Int64 == sc.Owner {
		return nil
	}
	who := "That machine is shared by the owner, and only the owner can use it."
	if owner.Valid && ownerName != "" {
		who = "That machine belongs to " + ownerName + "."
	}
	return forbidden("%s %s", who, c.useWords())
}

// ---- adding, listing, revoking and handing over ----

func deviceAdd(c *call) (any, error) {
	if err := c.mayAddDevice(); err != nil {
		return nil, err
	}
	var req api.NameReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if err := checkName("device", req.Name); err != nil {
		return nil, err
	}
	tok, err := c.addDevice(req.Name)
	if err != nil {
		return nil, err
	}
	return api.TokenResp{Name: req.Name, Token: tok}, nil
}

// mayAddDevice: owners and members add machines (they own what they add), and so does the admin
// token (what it adds is shared by the owner). Viewers, agents and AI apps do not.
func (c *call) mayAddDevice() error {
	if c.p.kind == kindAdmin {
		return nil
	}
	if c.p.kind == kindHuman && !c.p.operator && c.allow(ActDeviceAdd) == nil {
		if !c.h.allowRate("device.add:"+c.p.actor(), 20, 5, c.now) {
			return &apiError{429, "rate_limited", "Too many machines added in a short time. Wait a minute."}
		}
		return nil
	}
	return forbidden("only an owner or a member can add a machine")
}

func deviceList(c *call) (any, error) {
	if c.p.kind == kindDevice {
		return nil, forbidden("a machine does not list machines")
	}
	ds, err := store.ListDevices(c.tx, c.deviceScope())
	if err != nil {
		return nil, err
	}
	out := []api.Device{}
	for _, d := range ds {
		x := api.Device{Name: d.Name, Joined: d.Joined, Owner: d.Owner, Shared: d.Owner == "", Version: d.LinkVersion, Latest: d.LinkLatest}
		x.LastSeenAt, x.RevokedAt = store.TimePtr(d.LastSeen), store.TimePtr(d.Revoked)
		out = append(out, x)
	}
	return out, nil
}

// mayManageDevice: the owner and the admin token manage any machine; a member only their own.
func (c *call) mayManageDevice(owner sql.NullInt64, ownerName, what string) error {
	if c.isOwnerClass() {
		return nil
	}
	if c.p.kind != kindHuman || c.p.role != store.RoleMember {
		return forbidden("only an owner or a member can %s a machine", what)
	}
	id, err := c.humanID()
	if err != nil {
		return err
	}
	if owner.Valid && owner.Int64 == id {
		return nil
	}
	if owner.Valid && ownerName != "" {
		return forbidden("That machine belongs to %s. Only they or the owner can %s it.", ownerName, what)
	}
	return forbidden("That machine is shared by the owner. Only the owner can %s it.", what)
}

func (c *call) revokeDevice(name string) error {
	var owner sql.NullInt64
	var ownerName string
	err := c.tx.QueryRow(`SELECT d.owner_id, COALESCE(h.name, '') FROM device d LEFT JOIN human h ON h.id = d.owner_id WHERE d.name = ? AND d.revoked_at IS NULL`, name).Scan(&owner, &ownerName)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("no active device %q", name)
	}
	if err != nil {
		return err
	}
	if err := c.mayManageDevice(owner, ownerName, "revoke"); err != nil {
		return err
	}
	if _, err := c.tx.Exec(`UPDATE device SET revoked_at = ?, join_hash = NULL WHERE name = ? AND revoked_at IS NULL`, store.Millis(c.now), name); err != nil {
		return err
	}
	return c.audit("device.revoke", "device:"+name, map[string]any{"owner": ownerName})
}

func deviceRevoke(c *call) (any, error) {
	if c.p.kind == kindDevice {
		return nil, forbidden("a machine cannot revoke machines")
	}
	return nil, c.revokeDevice(c.r.PathValue("name"))
}

// assignDevice hands a machine to a person. Owners (and the admin token) only: it is how a machine
// shared by the owner, such as a build box, reaches a member.
func (c *call) assignDevice(name, to string) error {
	if !c.isOwnerClass() {
		return forbidden("only the owner can hand a machine to a person")
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return badRequest("say who the machine goes to")
	}
	target, err := store.HumanByName(c.tx, to)
	if err != nil {
		return err
	}
	if target == nil {
		return notFound("there is no person called %q", to)
	}
	if target.Role == store.RoleViewer {
		return badRequest("%s is a viewer, and viewers cannot use machines. Change their role first.", target.Name)
	}
	var from string
	err = c.tx.QueryRow(`SELECT COALESCE(h.name, '') FROM device d LEFT JOIN human h ON h.id = d.owner_id WHERE d.name = ? AND d.revoked_at IS NULL`, name).Scan(&from)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("no active device %q", name)
	}
	if err != nil {
		return err
	}
	if _, err := c.tx.Exec(`UPDATE device SET owner_id = ? WHERE name = ?`, target.ID, name); err != nil {
		return err
	}
	return c.audit("device.assign", "device:"+name, map[string]any{"from": from, "to": target.Name})
}

func deviceAssign(c *call) (any, error) {
	var req api.DeviceAssignReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	return nil, c.assignDevice(c.r.PathValue("name"), req.To)
}
