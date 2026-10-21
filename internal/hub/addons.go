package hub

import (
	"regexp"
	"strings"

	"handloom/internal/api"
	"handloom/internal/store"
)

// Add-ons: the hub registers what each computer reports and shows it. It never holds program bytes and
// never starts anything (docs/direction/addons.md).

const (
	maxAddonsReported = 100
	maxAddonManifest  = 64 << 10
)

var (
	addonNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	hashRe      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// addonReport replaces the calling computer's rows with what its link says it has. An add-on that is no
// longer reported is gone. Only a computer's own link reports.
func addonReport(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a computer's link reports its add-ons")
	}
	var req api.AddonReport
	if err := c.decode(&req); err != nil {
		return nil, err
	}
	if len(req.Addons) > maxAddonsReported {
		return nil, badRequest("too many add-ons in one report")
	}
	seen := map[string]bool{}
	for _, a := range req.Addons {
		switch {
		case !addonNameRe.MatchString(a.Name):
			return nil, badRequest("%q is not an add-on name", a.Name)
		case seen[a.Name]:
			return nil, badRequest("the add-on %s is reported twice", a.Name)
		case a.Status != "ready" && a.Status != "changed" && a.Status != "down" && a.Status != "missing":
			return nil, badRequest("the status of %s must be ready, changed, down or missing", a.Name)
		case (a.PackageHash != "" && !hashRe.MatchString(a.PackageHash)) || (a.ManifestHash != "" && !hashRe.MatchString(a.ManifestHash)):
			return nil, badRequest("a hash of %s is not a sha256", a.Name)
		case len(a.Version) > 64 || len(a.Detail) > 500:
			return nil, badRequest("the version or detail of %s is too long", a.Name)
		case len(a.Manifest) > maxAddonManifest:
			return nil, badRequest("the manifest of %s is too long", a.Name)
		}
		seen[a.Name] = true
	}
	if _, err := c.tx.Exec(`DELETE FROM addon WHERE device_id = ?`, c.p.deviceID); err != nil {
		return nil, err
	}
	for _, a := range req.Addons {
		if _, err := c.tx.Exec(`INSERT INTO addon(device_id, name, version, package_hash, manifest_hash, manifest, status, detail, reported_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.p.deviceID, a.Name, a.Version, a.PackageHash, a.ManifestHash, a.Manifest, a.Status, a.Detail, store.Millis(c.now)); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// addonRows reads add-ons on the computers the caller may see; name limits it to one add-on.
func (c *call) addonRows(name string, withManifest bool) ([]api.Addon, error) {
	where, args := c.deviceScope().Clause("d")
	q := `SELECT d.name, a.name, a.version, a.package_hash, a.manifest_hash, a.manifest, a.status, a.detail, a.reported_at
		FROM addon a JOIN device d ON d.id = a.device_id WHERE d.revoked_at IS NULL AND ` + where
	if name != "" {
		q += ` AND a.name = ?`
		args = append(args, name)
	}
	rows, err := c.tx.Query(q+` ORDER BY a.name, d.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Addon{}
	for rows.Next() {
		var x api.Addon
		var at int64
		if err := rows.Scan(&x.Computer, &x.Name, &x.Version, &x.PackageHash, &x.ManifestHash, &x.Manifest, &x.Status, &x.Detail, &at); err != nil {
			return nil, err
		}
		if !withManifest {
			x.Manifest = ""
		}
		x.ReportedAt = store.Time(at)
		out = append(out, x)
	}
	return out, rows.Err()
}

// addonList is every add-on on every computer the caller may see.
func addonList(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	return c.addonRows("", false)
}

// addonGet is one add-on across computers, with the manifest each computer reported.
func addonGet(c *call) (any, error) {
	if err := c.allow(ActRead); err != nil {
		return nil, err
	}
	name := strings.ToLower(c.r.PathValue("name"))
	rows, err := c.addonRows(name, true)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, notFound("no computer has reported an add-on called %s", name)
	}
	return rows, nil
}
