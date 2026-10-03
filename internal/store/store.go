// Package store owns the SQLite file: schema, migrations and a few shared helpers.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// migrations are applied in order; PRAGMA user_version records how many ran.
var migrations = []string{`
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);

CREATE TABLE project (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL
);

CREATE TABLE device (
  id              INTEGER PRIMARY KEY,
  name            TEXT NOT NULL UNIQUE,
  join_hash       TEXT,
  credential_hash TEXT,
  last_seen_at    INTEGER,
  revoked_at      INTEGER,
  created_at      INTEGER NOT NULL
);
CREATE INDEX device_credential ON device(credential_hash);

CREATE TABLE human (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  token_hash TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE agent (
  id            INTEGER PRIMARY KEY,
  project_id    INTEGER NOT NULL REFERENCES project(id),
  device_id     INTEGER NOT NULL REFERENCES device(id),
  name          TEXT NOT NULL UNIQUE,
  kind          TEXT NOT NULL,
  role          TEXT NOT NULL,
  wake_target   TEXT NOT NULL DEFAULT '',
  session_id    TEXT NOT NULL DEFAULT '',
  state         TEXT NOT NULL DEFAULT 'unknown',
  state_at      INTEGER NOT NULL,
  hook_msg_id   INTEGER NOT NULL DEFAULT 0, -- newest message id handed over by the end-of-turn hook
  registered_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX agent_one_lead ON agent(project_id) WHERE role = 'lead';

CREATE TABLE task (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id       INTEGER NOT NULL REFERENCES project(id),
  title            TEXT NOT NULL,
  body             TEXT NOT NULL DEFAULT '',
  status           TEXT NOT NULL,
  owner_agent_id   INTEGER REFERENCES agent(id),
  assigned_to      INTEGER REFERENCES agent(id),
  lease_expires_at INTEGER,
  depends_on       TEXT NOT NULL DEFAULT '[]',
  evidence         TEXT NOT NULL DEFAULT '[]',
  note             TEXT NOT NULL DEFAULT '',
  blocked_reason   TEXT NOT NULL DEFAULT '',
  reject_reason    TEXT NOT NULL DEFAULT '',
  created_by       TEXT NOT NULL,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL
);
CREATE INDEX task_lease ON task(status, lease_expires_at);

CREATE TABLE message (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id   INTEGER NOT NULL REFERENCES project(id),
  from_id      TEXT NOT NULL,
  to_agent_id  INTEGER NOT NULL REFERENCES agent(id),
  to_spec      TEXT NOT NULL,
  task_id      INTEGER REFERENCES task(id),
  body         TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  delivered_at INTEGER,
  delivered_by TEXT NOT NULL DEFAULT '',
  read_at      INTEGER
);
CREATE INDEX message_inbox ON message(to_agent_id, read_at);

CREATE TABLE event (
  seq        INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER,
  agent_id   INTEGER, -- the agent this event is for; NULL for board-wide events
  type       TEXT NOT NULL,
  payload    TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX event_agent ON event(agent_id, seq);

CREATE TABLE audit (
  seq        INTEGER PRIMARY KEY AUTOINCREMENT,
  actor      TEXT NOT NULL,
  action     TEXT NOT NULL,
  target     TEXT NOT NULL,
  payload    TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TRIGGER audit_no_update BEFORE UPDATE ON audit BEGIN SELECT RAISE(ABORT, 'audit is append-only'); END;
CREATE TRIGGER audit_no_delete BEFORE DELETE ON audit BEGIN SELECT RAISE(ABORT, 'audit is append-only'); END;
CREATE TRIGGER event_no_update BEFORE UPDATE ON event BEGIN SELECT RAISE(ABORT, 'event is append-only'); END;
CREATE TRIGGER event_no_delete BEFORE DELETE ON event BEGIN SELECT RAISE(ABORT, 'event is append-only'); END;
`, `
-- M2: the agent's working directory (headless resume runs there), and
-- tracking of assigned tasks that nobody claims.
ALTER TABLE agent ADD COLUMN dir TEXT NOT NULL DEFAULT '';
ALTER TABLE task ADD COLUMN claimable_at INTEGER;                       -- when the assignee could first claim it
ALTER TABLE task ADD COLUMN unclaimed_notified INTEGER NOT NULL DEFAULT 0;
`}

// Open opens (and migrates) the database at path. Use ":memory:" in tests.
func Open(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	if path != ":memory:" {
		dsn += "&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: the hub serialises all access, and an in-memory
	// database exists per connection.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Token prefixes tell the hub which table to look in.
const (
	PrefixAdmin  = "hva_"
	PrefixJoin   = "hvj_"
	PrefixDevice = "hvd_"
	PrefixHuman  = "hvh_"
)

// NewToken returns a random token with the given prefix (256 bits of entropy).
func NewToken(prefix string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// HashToken is what the hub stores. Tokens are random, so a plain hash is enough.
func HashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func Millis(t time.Time) int64 { return t.UnixMilli() }

func Time(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func TimePtr(ms sql.NullInt64) *time.Time {
	if !ms.Valid {
		return nil
	}
	t := Time(ms.Int64)
	return &t
}

// Init creates the default project and the admin token. It fails if the
// database is already initialised.
func Init(db *sql.DB, now time.Time) (adminToken string, err error) {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM meta WHERE key = 'admin_hash'`).Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		return "", fmt.Errorf("hub is already initialised")
	}
	adminToken = NewToken(PrefixAdmin)
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('admin_hash', ?)`, HashToken(adminToken)); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO project(name, created_at) VALUES ('default', ?)`, Millis(now)); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO audit(actor, action, target, payload, created_at) VALUES ('admin', 'hub.init', 'hub', '{}', ?)`, Millis(now)); err != nil {
		return "", err
	}
	return adminToken, tx.Commit()
}
