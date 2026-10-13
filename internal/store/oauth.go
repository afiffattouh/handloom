package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// The remote connector's records (D109). Everything secret is stored as a hash.

const (
	TokenCode    = "code"
	TokenAccess  = "access"
	TokenRefresh = "refresh"
)

type OAuthClient struct {
	ID           int64
	ClientID     string
	Name         string
	RedirectURIs []string
}

// CountOAuthClients is how many apps have registered.
func CountOAuthClients(q Querier) (int, error) {
	var n int
	err := q.QueryRow(`SELECT count(*) FROM oauth_client`).Scan(&n)
	return n, err
}

func CreateOAuthClient(q Querier, clientID, name string, uris []string, now int64) error {
	b, _ := json.Marshal(uris)
	_, err := q.Exec(`INSERT INTO oauth_client(client_id, name, redirect_uris, created_at) VALUES (?, ?, ?, ?)`, clientID, name, string(b), now)
	return err
}

// OAuthClientByID returns nil for an unknown client_id.
func OAuthClientByID(q Querier, clientID string) (*OAuthClient, error) {
	c := &OAuthClient{}
	var uris string
	err := q.QueryRow(`SELECT id, client_id, name, redirect_uris FROM oauth_client WHERE client_id = ?`, clientID).Scan(&c.ID, &c.ClientID, &c.Name, &uris)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(uris), &c.RedirectURIs)
	return c, nil
}

// PurgeOAuth removes what is no longer needed: expired tokens, grants that were
// allowed but never used for more than a day, and apps that registered but
// never completed a grant for clientAge milliseconds.
func PurgeOAuth(q Querier, now, clientAge int64) error {
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM oauth_token WHERE kind = 'access' AND expires_at < ?`, []any{now}},
		{`DELETE FROM oauth_token WHERE kind = 'code' AND expires_at < ? AND grant_id IN (SELECT id FROM oauth_grant WHERE activated_at IS NULL)`, []any{now}},
		{`DELETE FROM oauth_grant WHERE activated_at IS NULL AND created_at < ?`, []any{now - 24*3600*1000}},
		{`DELETE FROM oauth_client WHERE created_at < ? AND id NOT IN (SELECT client_id FROM oauth_grant WHERE activated_at IS NOT NULL)`, []any{now - clientAge}},
	} {
		if _, err := q.Exec(stmt.sql, stmt.args...); err != nil {
			return err
		}
	}
	return nil
}

func TouchOAuthClient(q Querier, id, now int64) error {
	_, err := q.Exec(`UPDATE oauth_client SET last_used_at = ? WHERE id = ?`, now, id)
	return err
}

// CreateGrant records that a person pressed Allow for an app. It is not "connected" until ActivateGrant.
func CreateGrant(q Querier, humanID, clientRowID, now int64) (int64, error) {
	res, err := q.Exec(`INSERT INTO oauth_grant(human_id, client_id, created_at) VALUES (?, ?, ?)`, humanID, clientRowID, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func ActivateGrant(q Querier, id, now int64) error {
	_, err := q.Exec(`UPDATE oauth_grant SET activated_at = COALESCE(activated_at, ?), last_used_at = ? WHERE id = ?`, now, now, id)
	return err
}

// RevokeGrant ends a grant and every token it issued. It reports whether it changed anything.
func RevokeGrant(q Querier, id, now int64) (bool, error) {
	res, err := q.Exec(`UPDATE oauth_grant SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, now, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Grant is a grant with its app and person.
type Grant struct {
	ID          int64
	HumanID     int64
	ClientRowID int64
	ClientID    string
	ClientName  string
	CreatedAt   int64
	ActivatedAt sql.NullInt64
	LastUsedAt  sql.NullInt64
	RevokedAt   sql.NullInt64
}

const grantSelect = `SELECT g.id, g.human_id, g.client_id, c.client_id, c.name, g.created_at, g.activated_at, g.last_used_at, g.revoked_at
	FROM oauth_grant g JOIN oauth_client c ON c.id = g.client_id `

func scanGrant(row interface{ Scan(...any) error }) (*Grant, error) {
	g := &Grant{}
	err := row.Scan(&g.ID, &g.HumanID, &g.ClientRowID, &g.ClientID, &g.ClientName, &g.CreatedAt, &g.ActivatedAt, &g.LastUsedAt, &g.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return g, err
}

func GrantByID(q Querier, id int64) (*Grant, error) {
	return scanGrant(q.QueryRow(grantSelect+`WHERE g.id = ?`, id))
}

// ConnectedApps are a person's grants that are in use and not revoked, newest first.
func ConnectedApps(q Querier, humanID int64) ([]Grant, error) {
	rows, err := q.Query(grantSelect+`WHERE g.human_id = ? AND g.activated_at IS NOT NULL AND g.revoked_at IS NULL ORDER BY g.activated_at DESC, g.id DESC`, humanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

type OAuthToken struct {
	ID            int64
	GrantID       int64
	Kind          string
	Hash          string
	ExpiresAt     int64
	UsedAt        sql.NullInt64
	CodeChallenge string
	RedirectURI   string
	Resource      string
}

func InsertOAuthToken(q Querier, t OAuthToken, now int64) error {
	_, err := q.Exec(`INSERT INTO oauth_token(grant_id, kind, hash, created_at, expires_at, code_challenge, redirect_uri, resource) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.GrantID, t.Kind, t.Hash, now, t.ExpiresAt, t.CodeChallenge, t.RedirectURI, t.Resource)
	return err
}

// OAuthTokenByHash returns nil when no token of that kind has that hash.
func OAuthTokenByHash(q Querier, hash, kind string) (*OAuthToken, error) {
	t := &OAuthToken{}
	err := q.QueryRow(`SELECT id, grant_id, kind, hash, expires_at, used_at, code_challenge, redirect_uri, resource FROM oauth_token WHERE hash = ? AND kind = ?`, hash, kind).
		Scan(&t.ID, &t.GrantID, &t.Kind, &t.Hash, &t.ExpiresAt, &t.UsedAt, &t.CodeChallenge, &t.RedirectURI, &t.Resource)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

// UseOAuthToken marks a code or refresh token used. It is true only for the one caller that got there first.
func UseOAuthToken(q Querier, id, now int64) (bool, error) {
	res, err := q.Exec(`UPDATE oauth_token SET used_at = ? WHERE id = ? AND used_at IS NULL`, now, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// AccessInfo is who an access token acts for.
type AccessInfo struct {
	Name, Role string
	GrantID    int64
	LastUsed   int64
	Hash       string // as stored, for a constant-time comparison by the caller
}

// AccessByHash resolves an access token to the person it acts for. It returns nil for an unknown,
// expired or revoked token, or one whose app never completed its grant.
func AccessByHash(q Querier, hash string, now int64) (*AccessInfo, error) {
	a := &AccessInfo{}
	var lu sql.NullInt64
	err := q.QueryRow(`SELECT h.name, h.role, g.id, g.last_used_at, t.hash
		FROM oauth_token t JOIN oauth_grant g ON g.id = t.grant_id JOIN human h ON h.id = g.human_id
		WHERE t.hash = ? AND t.kind = 'access' AND t.expires_at > ? AND g.revoked_at IS NULL AND g.activated_at IS NOT NULL`, hash, now).
		Scan(&a.Name, &a.Role, &a.GrantID, &lu, &a.Hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	a.LastUsed = lu.Int64
	return a, err
}

func TouchGrant(q Querier, id, now int64) error {
	_, err := q.Exec(`UPDATE oauth_grant SET last_used_at = ? WHERE id = ?`, now, id)
	return err
}
