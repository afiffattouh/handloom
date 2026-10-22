package hub

import (
	"database/sql"
	"errors"
	"regexp"
	"time"

	"handloom/internal/api"
	"handloom/internal/selfupdate"
	"handloom/internal/store"
)

// Updating a computer's link. The link reports what it is and what it could become; a person asks; the link
// downloads the release itself and says how it went. The hub holds a request and a result and never a program.

// updateRequestFor is how long a request waits for a computer that is not listening before it is dropped, so a
// laptop that was off does not update itself days later without anyone asking.
const updateRequestFor = time.Hour

var updateStates = map[string]bool{"waiting": true, "updating": true, "done": true, "failed": true}

var versionRe = regexp.MustCompile(`^[A-Za-z0-9._+\-]{1,64}$`)

// deviceSelfReport is the link telling the hub about itself.
func deviceSelfReport(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a computer's link reports its version")
	}
	var r api.LinkSelf
	if err := c.decodeUpTo(&r, 4<<10); err != nil {
		return nil, err
	}
	switch {
	case !versionRe.MatchString(r.Version):
		return nil, badRequest("the version is not a version")
	case r.Latest != "" && !selfupdate.IsRelease(r.Latest):
		return nil, badRequest("the latest release is not a release version")
	case len(r.Note) > 300 || len(r.Detail) > 500:
		return nil, badRequest("the note or detail is too long")
	case r.State != "" && !updateStates[r.State]:
		return nil, badRequest("the update state must be waiting, updating, done or failed")
	}
	if _, err := c.tx.Exec(`UPDATE device SET link_version = ?, link_latest = ?, link_can_update = ?, link_update_note = ? WHERE id = ?`,
		r.Version, r.Latest, r.CanUpdate, r.Note, c.p.deviceID); err != nil {
		return nil, err
	}
	if r.State == "" {
		return nil, nil
	}
	// A request the link finished, one way or the other, is over: nothing retries it.
	want := 0
	if r.State == "waiting" || r.State == "updating" {
		want = 1
	}
	if _, err := c.tx.Exec(`UPDATE device SET update_state = ?, update_detail = ?, update_want = ?, update_at = ? WHERE id = ?`,
		r.State, r.Detail, want, store.Millis(c.now), c.p.deviceID); err != nil {
		return nil, err
	}
	if r.State == "done" || r.State == "failed" {
		return nil, c.audit("device.update."+r.State, "device:"+c.p.name, map[string]any{"version": r.Version, "detail": r.Detail})
	}
	return nil, nil
}

// deviceUpdateWant tells the link whether a person asked it to update.
func deviceUpdateWant(c *call) (any, error) {
	if c.p.kind != kindDevice {
		return nil, forbidden("only a computer's link asks")
	}
	var want bool
	var state string
	var at sql.NullInt64
	if err := c.tx.QueryRow(`SELECT update_want, update_state, update_at FROM device WHERE id = ?`, c.p.deviceID).Scan(&want, &state, &at); err != nil {
		return nil, err
	}
	if want && state == "waiting" && at.Valid && c.now.Sub(store.Time(at.Int64)) > updateRequestFor {
		if _, err := c.tx.Exec(`UPDATE device SET update_want = 0, update_state = 'failed', update_detail = ? WHERE id = ?`,
			"The computer did not pick the request up within an hour, so it was dropped. Ask again when it is running.", c.p.deviceID); err != nil {
			return nil, err
		}
		want = false
	}
	return api.LinkUpdateWant{Want: want}, nil
}

// requestUpdate is a person asking a computer to update to the latest release its link has seen.
func (c *call) requestUpdate(name string) error {
	var (
		owner                  sql.NullInt64
		ownerName, cur, latest string
		can, wanted            bool
		note                   string
		seen                   sql.NullInt64
	)
	err := c.tx.QueryRow(`SELECT d.owner_id, COALESCE(h.name, ''), d.link_version, d.link_latest, d.link_can_update, d.link_update_note, d.update_want, d.last_seen_at
		FROM device d LEFT JOIN human h ON h.id = d.owner_id WHERE d.name = ? AND d.revoked_at IS NULL AND d.credential_hash IS NOT NULL`, name).
		Scan(&owner, &ownerName, &cur, &latest, &can, &note, &wanted, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("no joined computer %q", name)
	}
	if err != nil {
		return err
	}
	if err := c.mayManageDevice(owner, ownerName, "update"); err != nil {
		return err
	}
	switch {
	case cur == "":
		return badRequest("%s has not reported its version yet: its link must be running, and new enough to report it", name)
	case !can:
		if note == "" {
			note = "its link cannot replace itself"
		}
		return badRequest("%s cannot be updated from here: %s", name, note)
	case wanted:
		return badRequest("an update of %s is already under way", name)
	case latest == "" || !selfupdate.Newer(cur, latest):
		return badRequest("%s is already on the newest release it knows of (%s)", name, cur)
	}
	if _, err := c.tx.Exec(`UPDATE device SET update_want = 1, update_state = 'waiting', update_detail = '', update_at = ? WHERE name = ?`, store.Millis(c.now), name); err != nil {
		return err
	}
	return c.audit("device.update.request", "device:"+name, map[string]any{"from": cur, "to": latest})
}

func deviceUpdate(c *call) (any, error) {
	if c.p.kind == kindDevice {
		return nil, forbidden("a computer cannot update computers")
	}
	return nil, c.requestUpdate(c.r.PathValue("name"))
}
