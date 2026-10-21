package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMigrationSnapshotsAnExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "handloom.db")

	// Build a database at an older schema version by running only the first migration.
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(migrations[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO project(name, created_at) VALUES ('keep-me', 1)`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != len(migrations) {
		t.Fatalf("user_version %d, want %d", v, len(migrations))
	}
	snap := filepath.Join(dir, "backups", "pre-v"+strconv.Itoa(len(migrations))+".db")
	sdb, err := sql.Open("sqlite", "file:"+snap+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	var sv int
	var name string
	sdb.QueryRow(`PRAGMA user_version`).Scan(&sv)
	sdb.QueryRow(`SELECT name FROM project`).Scan(&name)
	if sv != 1 || name != "keep-me" {
		t.Fatalf("snapshot has version %d and project %q; want the pre-migration state", sv, name)
	}
	if fi, _ := os.Stat(snap); fi.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode %v", fi.Mode().Perm())
	}
}

func TestNewerSchemaIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handloom.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "refusing to touch it") {
		t.Fatalf("Open of a newer schema: %v", err)
	}
}

func TestBackupAndRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "handloom.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Init(db, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO project(name, created_at) VALUES ('second', 2)`); err != nil {
		t.Fatal(err)
	}
	bak := filepath.Join(dir, "out", "b.db")
	if err := Backup(db, bak); err != nil { // while the database is open and live
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM project WHERE name = 'second'`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if err := Restore(bak, path, false); err == nil {
		t.Fatal("Restore replaced an existing database without --force")
	}
	if err := Restore(bak, path, true); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	db.QueryRow(`SELECT count(*) FROM project WHERE name = 'second'`).Scan(&n)
	if n != 1 {
		t.Fatalf("restored database has %d 'second' projects, want 1", n)
	}

	// A corrupt file is refused.
	bad := filepath.Join(dir, "bad.db")
	os.WriteFile(bad, []byte("not a database"), 0o600)
	if err := Restore(bad, filepath.Join(dir, "other.db"), false); err == nil {
		t.Fatal("Restore accepted a corrupt backup")
	}
}

func TestResetAdminTokenReplacesTheOldOne(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResetAdminToken(db, time.Unix(1, 0)); err == nil {
		t.Fatal("reset before init")
	}
	old, err := Init(db, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := ResetAdminToken(db, time.Unix(2, 0))
	if err != nil || fresh == old || !strings.HasPrefix(fresh, PrefixAdmin) {
		t.Fatalf("reset: %q %v", fresh, err)
	}
	var h string
	db.QueryRow(`SELECT value FROM meta WHERE key = 'admin_hash'`).Scan(&h)
	if h != HashToken(fresh) {
		t.Fatal("the new token is not the stored one")
	}
}

// D108: a machine made before the change has no owner, and nothing is backfilled.
func TestMachinesOfAnOlderDatabaseHaveNoOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handloom.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:len(migrations)-1] { // the schema as the release before this one had it
		if _, err := old.Exec(m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := old.Exec(`PRAGMA user_version = ` + strconv.Itoa(len(migrations)-1)); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO device(name, credential_hash, created_at) VALUES ('build-box', 'x', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO human(name, token_hash, created_at) VALUES ('mia', 'y', 1)`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	all, err := ListDevices(db, DeviceScope{All: true})
	if err != nil || len(all) != 1 || all[0].OwnerID.Valid || all[0].Owner != "" {
		t.Fatalf("an older machine: %+v %v", all, err)
	}
	// Nobody but an owner sees it: a person's scope matches nothing of it, and a zero scope matches nothing.
	var mia int64
	db.QueryRow(`SELECT id FROM human WHERE name = 'mia'`).Scan(&mia)
	for _, sc := range []DeviceScope{{Owner: mia}, {}} {
		if got, err := ListDevices(db, sc); err != nil || len(got) != 0 {
			t.Fatalf("scope %+v sees %+v %v", sc, got, err)
		}
	}
	// And a machine can be given an owner afterwards.
	if _, err := db.Exec(`UPDATE device SET owner_id = ?`, mia); err != nil {
		t.Fatal(err)
	}
	if got, _ := ListDevices(db, DeviceScope{Owner: mia}); len(got) != 1 || got[0].Owner != "mia" {
		t.Fatalf("after the hand-over: %+v", got)
	}
}

// The add-on table is the newest migration: a database from the release before has none, and gets it.
func TestAddonTableArrivesWithTheLastMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handloom.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:len(migrations)-1] {
		if _, err := old.Exec(m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := old.Exec(`PRAGMA user_version = ` + strconv.Itoa(len(migrations)-1)); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO device(name, credential_hash, created_at) VALUES ('desk', 'x', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`SELECT 1 FROM addon`); err == nil {
		t.Fatal("the older schema already has an addon table")
	}
	old.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ins := func(device int, name, status string) error {
		_, err := db.Exec(`INSERT INTO addon(device_id, name, status, reported_at) VALUES (?, ?, ?, 1)`, device, name, status)
		return err
	}
	for _, s := range []string{"ready", "changed", "down", "missing"} {
		if err := ins(1, "echo-"+s, s); err != nil {
			t.Errorf("status %s refused: %v", s, err)
		}
	}
	if err := ins(1, "x", "running"); err == nil {
		t.Error("a status outside the four was accepted")
	}
	if err := ins(1, "echo-ready", "ready"); err == nil {
		t.Error("two rows for one computer and name were accepted")
	}
	if err := ins(99, "echo", "ready"); err == nil {
		t.Error("a row for a computer that does not exist was accepted")
	}
}
