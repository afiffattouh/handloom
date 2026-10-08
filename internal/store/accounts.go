package store

import (
	"database/sql"
	"errors"
)

// Human roles in the web UI. An API-only human is a member.
const (
	RoleOwner  = "owner"
	RoleMember = "member"
	RoleViewer = "viewer"
)

// Querier is what both *sql.DB and *sql.Tx offer.
type Querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

type Human struct {
	ID           int64
	Name         string
	Role         string
	PasswordHash string // empty: no web login
}

func scanHuman(row *sql.Row) (*Human, error) {
	h := &Human{}
	var pw sql.NullString
	err := row.Scan(&h.ID, &h.Name, &h.Role, &pw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	h.PasswordHash = pw.String
	return h, nil
}

const humanSelect = `SELECT id, name, role, password_hash FROM human `

// HumanByName returns nil when there is no such human.
func HumanByName(q Querier, name string) (*Human, error) {
	return scanHuman(q.QueryRow(humanSelect+`WHERE name = ?`, name))
}

func HumanByID(q Querier, id int64) (*Human, error) {
	return scanHuman(q.QueryRow(humanSelect+`WHERE id = ?`, id))
}

// OwnerExists reports whether the hub has an owner. The setup wizard is open
// exactly while it does not.
func OwnerExists(q Querier) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT count(*) FROM human WHERE role = 'owner' AND password_hash IS NOT NULL`).Scan(&n)
	return n > 0, err
}

// CreateWebHuman adds a human who can sign in. The API token column is
// filled with a random hash nobody holds: such a human has no API token
// until one is issued.
func CreateWebHuman(q Querier, name, role, passwordHash string, now int64) (int64, error) {
	res, err := q.Exec(`INSERT INTO human(name, token_hash, created_at, password_hash, role) VALUES (?, ?, ?, ?, ?)`,
		name, HashToken(NewToken(PrefixHuman)), now, passwordHash, role)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func SetPassword(q Querier, humanID int64, passwordHash string) error {
	_, err := q.Exec(`UPDATE human SET password_hash = ? WHERE id = ?`, passwordHash, humanID)
	return err
}

func MetaGet(q Querier, key string) (string, bool, error) {
	var v string
	err := q.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func MetaSet(q Querier, key, value string) error {
	_, err := q.Exec(`INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func MetaDelete(q Querier, key string) error {
	_, err := q.Exec(`DELETE FROM meta WHERE key = ?`, key)
	return err
}

// Session is one signed-in browser. Only the hash of the id is stored.
type Session struct {
	IDHash     string
	HumanID    int64
	CSRF       string
	CreatedAt  int64
	LastSeenAt int64
	ExpiresAt  int64
}

func CreateSession(q Querier, s Session, ip string) error {
	_, err := q.Exec(`INSERT INTO web_session(id_hash, human_id, csrf, created_at, last_seen_at, expires_at, ip) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.IDHash, s.HumanID, s.CSRF, s.CreatedAt, s.LastSeenAt, s.ExpiresAt, ip)
	return err
}

// SessionByHash returns nil when there is none.
func SessionByHash(q Querier, idHash string) (*Session, error) {
	s := &Session{}
	err := q.QueryRow(`SELECT id_hash, human_id, csrf, created_at, last_seen_at, expires_at FROM web_session WHERE id_hash = ?`, idHash).
		Scan(&s.IDHash, &s.HumanID, &s.CSRF, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func TouchSession(q Querier, idHash string, now int64) error {
	_, err := q.Exec(`UPDATE web_session SET last_seen_at = ? WHERE id_hash = ?`, now, idHash)
	return err
}

func DeleteSession(q Querier, idHash string) error {
	_, err := q.Exec(`DELETE FROM web_session WHERE id_hash = ?`, idHash)
	return err
}

// DeleteSessionsFor signs a human out everywhere (password change, role change).
func DeleteSessionsFor(q Querier, humanID int64) error {
	_, err := q.Exec(`DELETE FROM web_session WHERE human_id = ?`, humanID)
	return err
}

// PurgeSessions removes sessions past their absolute expiry or idle for longer than idleMs.
func PurgeSessions(q Querier, now, idleMs int64) error {
	_, err := q.Exec(`DELETE FROM web_session WHERE expires_at < ? OR last_seen_at < ?`, now, now-idleMs)
	return err
}

// CreateInvitedHuman adds a human with no password yet; they set one through an invite.
func CreateInvitedHuman(q Querier, name, role string, now int64) (int64, error) {
	res, err := q.Exec(`INSERT INTO human(name, token_hash, created_at, role) VALUES (?, ?, ?, ?)`,
		name, HashToken(NewToken(PrefixHuman)), now, role)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func SetRole(q Querier, humanID int64, role string) error {
	_, err := q.Exec(`UPDATE human SET role = ? WHERE id = ?`, role, humanID)
	return err
}

// ListHumans returns everybody, owners first.
func ListHumans(q Querier) ([]Human, error) {
	rows, err := q.Query(`SELECT id, name, role, password_hash FROM human ORDER BY CASE role WHEN 'owner' THEN 0 WHEN 'member' THEN 1 ELSE 2 END, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Human
	for rows.Next() {
		var h Human
		var pw sql.NullString
		if err := rows.Scan(&h.ID, &h.Name, &h.Role, &pw); err != nil {
			return nil, err
		}
		h.PasswordHash = pw.String
		out = append(out, h)
	}
	return out, rows.Err()
}

// ReplaceHumanToken gives a human a new API token hash (the old one stops working).
func ReplaceHumanToken(q Querier, humanID int64, tokenHash string) error {
	_, err := q.Exec(`UPDATE human SET token_hash = ? WHERE id = ?`, tokenHash, humanID)
	return err
}

// Invites. Only the hash of the token is stored; a new invite replaces older ones for that human.

func CreateInvite(q Querier, tokenHash string, humanID, now, expires int64) error {
	if _, err := q.Exec(`DELETE FROM invite WHERE human_id = ?`, humanID); err != nil {
		return err
	}
	_, err := q.Exec(`INSERT INTO invite(token_hash, human_id, created_at, expires_at) VALUES (?, ?, ?, ?)`, tokenHash, humanID, now, expires)
	return err
}

// InviteHuman returns the invited human, or nil if the invite is unknown or expired.
func InviteHuman(q Querier, tokenHash string, now int64) (*Human, error) {
	var id int64
	err := q.QueryRow(`SELECT human_id FROM invite WHERE token_hash = ? AND expires_at > ?`, tokenHash, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return HumanByID(q, id)
}

func DeleteInvite(q Querier, tokenHash string) error {
	_, err := q.Exec(`DELETE FROM invite WHERE token_hash = ?`, tokenHash)
	return err
}

// DevicesWithAgents lists devices for the devices page.
type DeviceInfo struct {
	Name      string
	Joined    bool
	LastSeen  sql.NullInt64
	Revoked   sql.NullInt64
	JoinUntil sql.NullInt64
	Agents    int
}

func ListDevices(q Querier) ([]DeviceInfo, error) {
	rows, err := q.Query(`SELECT d.name, d.credential_hash IS NOT NULL, d.last_seen_at, d.revoked_at,
		CASE WHEN d.join_hash IS NOT NULL THEN d.join_expires_at END, (SELECT count(*) FROM agent a WHERE a.device_id = d.id)
		FROM device d ORDER BY d.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceInfo
	for rows.Next() {
		var d DeviceInfo
		if err := rows.Scan(&d.Name, &d.Joined, &d.LastSeen, &d.Revoked, &d.JoinUntil, &d.Agents); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetOperatorToken gives a person a token for their AI app (the old one stops working).
func SetOperatorToken(q Querier, humanID int64, tokenHash string, now int64) error {
	_, err := q.Exec(`INSERT INTO operator_token(human_id, token_hash, created_at) VALUES (?, ?, ?)
		ON CONFLICT(human_id) DO UPDATE SET token_hash = excluded.token_hash, created_at = excluded.created_at`, humanID, tokenHash, now)
	return err
}

// RemoveOperatorToken takes an AI app's access away.
func RemoveOperatorToken(q Querier, humanID int64) error {
	_, err := q.Exec(`DELETE FROM operator_token WHERE human_id = ?`, humanID)
	return err
}

// HasOperatorToken says whether a person has an AI app connected.
func HasOperatorToken(q Querier, humanID int64) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT count(*) FROM operator_token WHERE human_id = ?`, humanID).Scan(&n)
	return n > 0, err
}
