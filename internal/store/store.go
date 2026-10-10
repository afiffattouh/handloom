// Package store owns the SQLite file: schema, migrations and a few shared helpers.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
`, `
-- A: escalations, the one channel from the lead to the human.
CREATE TABLE escalation (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id    INTEGER NOT NULL REFERENCES project(id),
  from_agent_id INTEGER NOT NULL REFERENCES agent(id),
  task_id       INTEGER REFERENCES task(id),
  question      TEXT NOT NULL,
  options       TEXT NOT NULL DEFAULT '[]',
  answer        TEXT,
  answered_by   TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL,
  answered_at   INTEGER
);
CREATE INDEX escalation_open ON escalation(project_id, answered_at);
`, `
-- A: web accounts. A human with a password_hash can sign in to the web UI;
-- the API token stays as it was. role: owner | member | viewer.
ALTER TABLE human ADD COLUMN password_hash TEXT;
ALTER TABLE human ADD COLUMN role TEXT NOT NULL DEFAULT 'member';
CREATE TABLE web_session (
  id_hash      TEXT PRIMARY KEY, -- sha256 of the random session id; the id lives only in the cookie
  human_id     INTEGER NOT NULL REFERENCES human(id),
  csrf         TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  ip           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX web_session_human ON web_session(human_id);
`, `
-- A: join tokens expire (NULL on old rows means they never did), and members
-- are invited with a one-time link instead of a password somebody else chose.
ALTER TABLE device ADD COLUMN join_expires_at INTEGER;
CREATE TABLE invite (
  token_hash TEXT PRIMARY KEY,
  human_id   INTEGER NOT NULL REFERENCES human(id),
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
`, `
-- B1: a job is a root task (kind 'job'). Tasks made for it carry its id; an
-- agent can belong to a job, and each job has its own lead. Agents and tasks
-- without a job behave as before: one project-level lead.
ALTER TABLE task ADD COLUMN kind TEXT NOT NULL DEFAULT 'task';
ALTER TABLE task ADD COLUMN job_id INTEGER REFERENCES task(id);
ALTER TABLE task ADD COLUMN parent_id INTEGER REFERENCES task(id);
ALTER TABLE task ADD COLUMN confidential INTEGER NOT NULL DEFAULT 0;
CREATE INDEX task_job ON task(job_id);
ALTER TABLE agent ADD COLUMN job_id INTEGER REFERENCES task(id);
DROP INDEX agent_one_lead;
CREATE UNIQUE INDEX agent_one_lead ON agent(project_id, COALESCE(job_id, 0)) WHERE role = 'lead';
`, `
-- B2: run tokens. An agent can hold a token (only its hash is stored) that
-- proves a request comes from the process the link started for it, not from
-- anything on the device that merely knows its name. The audit log records
-- how each request was authenticated.
ALTER TABLE agent ADD COLUMN run_token_hash TEXT;
ALTER TABLE agent ADD COLUMN run_token_at INTEGER;
ALTER TABLE audit ADD COLUMN via TEXT NOT NULL DEFAULT '';
`, `
-- B3: an agent with a terminal has a liveness lease that its link renews while
-- the terminal exists. NULL: no lease (headless and shell agents).
ALTER TABLE agent ADD COLUMN lease_expires_at INTEGER;
`, `
-- C1: spawn. A lead or a human asks for an agent to be started on a device;
-- the device's link starts it in a terminal it owns and reports back.
CREATE TABLE spawn (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL REFERENCES project(id),
  device_id  INTEGER NOT NULL REFERENCES device(id),
  job_id     INTEGER REFERENCES task(id),
  name       TEXT NOT NULL,
  kind       TEXT NOT NULL,
  model      TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL,  -- pending | launching | started | failed
  pane       TEXT NOT NULL DEFAULT '',
  error      TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX spawn_live_name ON spawn(name) WHERE status IN ('pending', 'launching', 'started');
CREATE INDEX spawn_device ON spawn(device_id, status);
`, `
-- C2: profiles. Immutable and versioned: a change is a new version, and a
-- spawn is pinned to the exact version and hash it was asked for.
CREATE TABLE profile (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  name       TEXT NOT NULL,
  version    INTEGER NOT NULL,
  hash       TEXT NOT NULL,
  spec       TEXT NOT NULL,
  created_by TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE (name, version)
);
CREATE TRIGGER profile_no_update BEFORE UPDATE ON profile BEGIN SELECT RAISE(ABORT, 'a profile version is immutable'); END;
CREATE TRIGGER profile_no_delete BEFORE DELETE ON profile BEGIN SELECT RAISE(ABORT, 'a profile version is immutable'); END;
ALTER TABLE spawn ADD COLUMN profile_name TEXT NOT NULL DEFAULT '';
ALTER TABLE spawn ADD COLUMN profile_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE spawn ADD COLUMN profile_hash TEXT NOT NULL DEFAULT '';
`, `
-- D1: repo jobs. A job may name a repository on one device and a command that
-- checks finished work; every agent spawned into it gets its own git worktree
-- there. A spawn may be for the job's lead, which makes the job start itself.
ALTER TABLE task ADD COLUMN repo TEXT NOT NULL DEFAULT '';
ALTER TABLE task ADD COLUMN verify TEXT NOT NULL DEFAULT '';
ALTER TABLE task ADD COLUMN device_id INTEGER REFERENCES device(id);
ALTER TABLE spawn ADD COLUMN role TEXT NOT NULL DEFAULT 'worker';
ALTER TABLE spawn ADD COLUMN repo TEXT NOT NULL DEFAULT '';
`, `
-- D3: what a device found when it ran a job's verify command on a submitted task.
-- One row per task for its current submission (a new submit clears it).
CREATE TABLE task_check (
  task_id   INTEGER PRIMARY KEY REFERENCES task(id),
  agent     TEXT NOT NULL,
  device_id INTEGER NOT NULL REFERENCES device(id),
  command   TEXT NOT NULL,
  exit_code INTEGER NOT NULL,
  timed_out INTEGER NOT NULL DEFAULT 0,
  tail      TEXT NOT NULL DEFAULT '',
  sha256    TEXT NOT NULL DEFAULT '',
  at        INTEGER NOT NULL
);
`, `
-- D7: an accepted task's branch is merged into the job's integration branch by the device that holds the repository.
CREATE TABLE merge (
  id         INTEGER PRIMARY KEY,
  task_id    INTEGER NOT NULL REFERENCES task(id),
  job_id     INTEGER NOT NULL REFERENCES task(id),
  device_id  INTEGER NOT NULL REFERENCES device(id),
  agent      TEXT NOT NULL,
  status     TEXT NOT NULL DEFAULT 'pending',
  detail     TEXT NOT NULL DEFAULT '',
  head       TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  done_at    INTEGER
);
CREATE INDEX merge_pending ON merge(device_id, status);
CREATE INDEX merge_task ON merge(task_id);
`, `
-- F: a job may name a knowledge repository (read by every agent, extended by proposals on a branch of its own) and a base branch.
ALTER TABLE task ADD COLUMN knowledge TEXT NOT NULL DEFAULT '';
ALTER TABLE task ADD COLUMN base TEXT NOT NULL DEFAULT '';
ALTER TABLE spawn ADD COLUMN knowledge TEXT NOT NULL DEFAULT '';
ALTER TABLE spawn ADD COLUMN base TEXT NOT NULL DEFAULT '';
-- What the device found when it gathered an agent's proposed notes onto the job's branch. Counts only: the notes stay in the repository.
CREATE TABLE kcollect (
  id         INTEGER PRIMARY KEY,
  job_id     INTEGER NOT NULL REFERENCES task(id),
  device_id  INTEGER NOT NULL REFERENCES device(id),
  agent      TEXT NOT NULL,
  status     TEXT NOT NULL DEFAULT 'pending',
  notes      INTEGER NOT NULL DEFAULT 0,
  detail     TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  done_at    INTEGER
);
CREATE INDEX kcollect_pending ON kcollect(device_id, status);
CREATE INDEX kcollect_job ON kcollect(job_id);
`, `
-- The command center asks the audit log "what happened to this kind of thing since then".
CREATE INDEX audit_action_time ON audit(action, created_at);
CREATE INDEX audit_target_time ON audit(target, created_at);

-- What agents have used, read by each device from the agent CLI's own log and reported as running totals. Counts only: no text.
CREATE TABLE usage_sample (
  id          INTEGER PRIMARY KEY,
  agent       TEXT NOT NULL,
  model       TEXT NOT NULL,
  input       INTEGER NOT NULL,  -- tokens that were not served from a cache
  output      INTEGER NOT NULL,
  cache_read  INTEGER NOT NULL,
  cache_write INTEGER NOT NULL,
  at          INTEGER NOT NULL
);
CREATE INDEX usage_agent ON usage_sample(agent, model, at);
-- Prices the owner entered, in US dollars per million tokens. There are no defaults: with no price there is no cost.
CREATE TABLE price (
  model       TEXT PRIMARY KEY,  -- a model name or the start of one
  input       REAL NOT NULL,
  output      REAL NOT NULL,
  cache_read  REAL NOT NULL DEFAULT 0,
  cache_write REAL NOT NULL DEFAULT 0
);
`, `
-- A token for a person's own AI app: it acts as that person but the hub only lets it read and start work, never approve. One per person; a new one replaces it.
CREATE TABLE operator_token (
  human_id   INTEGER PRIMARY KEY REFERENCES human(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL
);
`, `
-- What a task's owner leaves for whoever picks the task up next: where it got to, what was tried, the next step, how to verify.
-- Append-only: the latest row is the current note.
CREATE TABLE handoff (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id    INTEGER NOT NULL REFERENCES task(id),
  author     TEXT NOT NULL,
  kind       TEXT NOT NULL,           -- checkpoint | submit | release
  done       TEXT NOT NULL,
  tried      TEXT NOT NULL DEFAULT '',
  next       TEXT NOT NULL DEFAULT '',
  verify     TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX handoff_task ON handoff(task_id, id);
CREATE TRIGGER handoff_no_update BEFORE UPDATE ON handoff BEGIN SELECT RAISE(ABORT, 'handoff is append-only'); END;
CREATE TRIGGER handoff_no_delete BEFORE DELETE ON handoff BEGIN SELECT RAISE(ABORT, 'handoff is append-only'); END;
`, `
CREATE TABLE lesson (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id        INTEGER,
  proposed_by   TEXT NOT NULL,
  profile       TEXT NOT NULL,
  skill         TEXT NOT NULL,
  text          TEXT NOT NULL,
  why           TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'proposed',
  decided_by    TEXT NOT NULL DEFAULT '',
  decided_at    INTEGER,
  decision_note TEXT NOT NULL DEFAULT '',
  new_version   INTEGER,
  created_at    INTEGER NOT NULL
);
CREATE INDEX lesson_status ON lesson(status, id);
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
	if err := migrate(db, path); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB, path string) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("the database has schema version %d but this handloom knows only %d: refusing to touch it; run a newer handloom, or restore a backup", v, len(migrations))
	}
	// An existing database that is about to change gets a snapshot first, so
	// an upgrade can be rolled back by restoring it with the old binary.
	if v > 0 && v < len(migrations) && path != ":memory:" {
		snap := filepath.Join(filepath.Dir(path), "backups", fmt.Sprintf("pre-v%d.db", len(migrations)))
		if err := Backup(db, snap); err != nil {
			return fmt.Errorf("snapshot before migration: %w", err)
		}
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
	PrefixAdmin    = "hva_"
	PrefixJoin     = "hvj_"
	PrefixDevice   = "hvd_"
	PrefixHuman    = "hvh_"
	PrefixOperator = "hvo_" // a person's AI app: reads and starts work, cannot approve
	PrefixRun      = "hvr_" // an agent's run token
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

// Backup writes a consistent copy of the live database to dest with VACUUM
// INTO. It is safe while the hub runs; copying handloom.db and its WAL by hand is not.
// dest must not exist: an old snapshot is replaced.
func Backup(db *sql.DB, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return err
	}
	if _, err := db.Exec(`VACUUM INTO '` + strings.ReplaceAll(dest, "'", "''") + `'`); err != nil {
		return err
	}
	return os.Chmod(dest, 0o600)
}

// Restore puts a backup in place of the database at dst. The hub must be
// stopped. The backup is checked first: integrity and schema version.
func Restore(src, dst string, force bool) error {
	if _, err := os.Stat(dst); err == nil && !force {
		return fmt.Errorf("%s exists; stop the hub and use --force to replace it", dst)
	}
	db, err := sql.Open("sqlite", "file:"+src+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var check string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		return fmt.Errorf("the backup failed its integrity check: %v %s", err, check)
	}
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("the backup has schema version %d; this handloom knows %d", v, len(migrations))
	}
	tmp := dst + ".restore"
	if err := Backup(db, tmp); err != nil {
		return err
	}
	for _, ext := range []string{"-wal", "-shm"} {
		os.Remove(dst + ext) // stale journals of the replaced database
	}
	return os.Rename(tmp, dst)
}

// ResetAdminToken replaces the admin token and returns the new one. It is the
// recovery path for a lost token: whoever can open the database can run it.
func ResetAdminToken(db *sql.DB, now time.Time) (string, error) {
	tok := NewToken(PrefixAdmin)
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE meta SET value = ? WHERE key = 'admin_hash'`, HashToken(tok))
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", fmt.Errorf("this hub is not initialised yet")
	}
	if _, err := tx.Exec(`INSERT INTO audit(actor, action, target, payload, created_at) VALUES ('admin', 'admin.token.reset', 'hub', '{}', ?)`, Millis(now)); err != nil {
		return "", err
	}
	return tok, tx.Commit()
}
