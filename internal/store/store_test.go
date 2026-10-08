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
