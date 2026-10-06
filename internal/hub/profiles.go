package hub

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"handloom/internal/api"
	"handloom/internal/profile"
	"handloom/internal/store"
)

// Profiles are the hub's library of "what an agent may do and know". They are
// written only by the owner (or the admin): a profile can allow a shell, so
// writing one widens what agents can do, like adding a device does. A change
// is a new version; a spawn is pinned to one version and its hash.

// ownerOnly lets the admin token and an owner through.
func (c *call) ownerOnly(what string) error {
	switch {
	case c.p.kind == kindAdmin:
		return nil
	case c.p.kind == kindHuman && c.p.role == store.RoleOwner:
		return nil
	}
	return forbidden("%s is for the owner", what)
}

type profileRow struct {
	name      string
	version   int
	hash      string
	spec      profile.Spec
	createdBy string
	created   int64
}

func (p *profileRow) info() api.ProfileInfo {
	return api.ProfileInfo{Name: p.name, Version: p.version, Hash: p.hash, Description: p.spec.Description,
		Kind: p.spec.Kind, Runtime: p.spec.Runtime, Tools: append([]string{}, p.spec.Tools.Allow...),
		Skills: profile.SkillNames(&p.spec), CreatedBy: p.createdBy, CreatedAt: store.Time(p.created)}
}

func (p *profileRow) full() api.ProfileFull {
	return api.ProfileFull{ProfileInfo: p.info(), Spec: p.spec}
}

func scanProfile(s scanner) (*profileRow, error) {
	p := &profileRow{}
	var spec string
	err := s.Scan(&p.name, &p.version, &p.hash, &spec, &p.createdBy, &p.created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, json.Unmarshal([]byte(spec), &p.spec)
}

const profileSelect = `SELECT name, version, hash, spec, created_by, created_at FROM profile `

// profileVersion returns the named version, or the newest when version is 0.
func (c *call) profileVersion(name string, version int) (*profileRow, error) {
	var row *sql.Row
	if version > 0 {
		row = c.tx.QueryRow(profileSelect+`WHERE name = ? AND version = ?`, name, version)
	} else {
		row = c.tx.QueryRow(profileSelect+`WHERE name = ? ORDER BY version DESC LIMIT 1`, name)
	}
	p, err := scanProfile(row)
	if err != nil {
		return nil, err
	}
	if p == nil {
		if version > 0 {
			return nil, notFound("no profile %s@%d", name, version)
		}
		return nil, notFound("no profile %q", name)
	}
	return p, nil
}

func profileNew(c *call) (any, error) {
	if err := c.ownerOnly("writing profiles"); err != nil {
		return nil, err
	}
	var req api.ProfileReq
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	p, err := c.saveProfile(req.Name, req.Spec)
	if err != nil {
		return nil, err
	}
	return p.full(), nil
}

// saveProfile validates and stores a profile as a new version, or returns the
// current one when nothing changed. The caller has checked who is allowed.
func (c *call) saveProfile(name string, spec profile.Spec) (*profileRow, error) {
	req := api.ProfileReq{Name: name}
	if !profile.ValidName(req.Name) {
		return nil, badRequest("bad profile name %q: use letters, digits, '.', '_' or '-', at most 40 characters", req.Name)
	}
	if bad := profile.Normalize(&spec); len(bad) > 0 {
		return nil, badRequest("the profile has problems: %s", strings.Join(bad, "; "))
	}
	hash := profile.Hash(&spec)
	cur, err := scanProfile(c.tx.QueryRow(profileSelect+`WHERE name = ? ORDER BY version DESC LIMIT 1`, req.Name))
	if err != nil {
		return nil, err
	}
	if cur != nil && cur.hash == hash {
		return cur, nil // nothing changed: no new version
	}
	version := 1
	if cur != nil {
		version = cur.version + 1
	}
	b, _ := json.Marshal(&spec)
	if _, err := c.tx.Exec(`INSERT INTO profile(name, version, hash, spec, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		req.Name, version, hash, string(b), c.p.actor(), store.Millis(c.now)); err != nil {
		return nil, err
	}
	if err := c.audit("profile.new", fmt.Sprintf("profile:%s@%d", req.Name, version),
		map[string]any{"hash": hash, "kind": spec.Kind, "runtime": spec.Runtime, "tools": spec.Tools.Allow, "deny": spec.Tools.DenyCommands, "skills": profile.SkillNames(&spec)}); err != nil {
		return nil, err
	}
	return c.profileVersion(req.Name, version)
}

func profileList(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	rows, err := c.tx.Query(profileSelect + `WHERE version = (SELECT max(version) FROM profile p2 WHERE p2.name = profile.name) ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.ProfileInfo{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p.info())
	}
	return out, rows.Err()
}

func profileGet(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	version, _ := strconv.Atoi(c.r.URL.Query().Get("version"))
	p, err := c.profileVersion(c.r.PathValue("name"), version)
	if err != nil {
		return nil, err
	}
	return p.full(), nil
}

func profileVersions(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	rows, err := c.tx.Query(profileSelect+`WHERE name = ? ORDER BY version`, c.r.PathValue("name"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.ProfileInfo{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p.info())
	}
	if len(out) == 0 {
		return nil, notFound("no profile %q", c.r.PathValue("name"))
	}
	return out, rows.Err()
}

// parseProfileRef splits "name" or "name@3".
func parseProfileRef(ref string) (string, int, error) {
	name, v, found := strings.Cut(ref, "@")
	if !profile.ValidName(name) {
		return "", 0, badRequest("bad profile %q", ref)
	}
	if !found {
		return name, 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return "", 0, badRequest("bad profile version in %q", ref)
	}
	return name, n, nil
}

// spawnProfile is what a device fetches for the spawn it is starting: the
// exact version and hash the request was pinned to, even if the profile has
// had new versions since. Only the spawn's own device gets it.
func spawnProfile(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a device may fetch the profile of a spawn")
	}
	id, err := c.pathID()
	if err != nil {
		return nil, err
	}
	s, err := c.spawn(id)
	if err != nil {
		return nil, err
	}
	if s.deviceID != c.p.deviceID {
		return nil, forbidden("spawn %d belongs to another device", id)
	}
	if s.profileName == "" {
		return nil, notFound("spawn %d has no profile", id)
	}
	p, err := c.profileVersion(s.profileName, s.profileVersion)
	if err != nil {
		return nil, err
	}
	if p.hash != s.profileHash {
		return nil, conflict("profile %s@%d does not match the hash the spawn was pinned to", p.name, p.version)
	}
	return p.full(), nil
}
