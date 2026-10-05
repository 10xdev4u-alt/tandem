package sqlitesession_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/10xdev4u-alt/tandem/internal/sqlitesession"
)

const accountsDDL = `CREATE TABLE accounts (
	id    INTEGER PRIMARY KEY,
	owner TEXT NOT NULL,
	cents INTEGER NOT NULL
)`

// notes has no primary key on purpose. SQLite will attach it and then record
// nothing for it, which is the silent data loss this test exists to catch.
const notesDDL = `CREATE TABLE notes (
	body TEXT NOT NULL
)`

func openDB(t *testing.T, ddl ...string) *sqlitesession.DB {
	t.Helper()
	db, err := sqlitesession.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, s := range ddl {
		if err := db.Exec(s); err != nil {
			t.Fatalf("Exec(%q): %v", s, err)
		}
	}
	return db
}

func TestHasPrimaryKey(t *testing.T) {
	db := openDB(t, accountsDDL, notesDDL)

	for _, tc := range []struct {
		table string
		want  bool
	}{
		{"accounts", true},
		{"notes", false},
		{"does_not_exist", false},
	} {
		got, err := db.HasPrimaryKey(tc.table)
		if err != nil {
			t.Fatalf("HasPrimaryKey(%q): %v", tc.table, err)
		}
		if got != tc.want {
			t.Errorf("HasPrimaryKey(%q) = %v, want %v", tc.table, got, tc.want)
		}
	}
}

func TestHasPrimaryKeyRejectsEmptyName(t *testing.T) {
	db := openDB(t)
	if _, err := db.HasPrimaryKey(""); err == nil {
		t.Fatal("expected an error for an empty table name")
	}
}

func TestTableNamesSkipsInternalTables(t *testing.T) {
	db := openDB(t, accountsDDL, notesDDL)

	names, err := db.TableNames()
	if err != nil {
		t.Fatalf("TableNames: %v", err)
	}
	if len(names) != 2 || names[0] != "accounts" || names[1] != "notes" {
		t.Fatalf("TableNames = %v, want [accounts notes]", names)
	}
}

// TestStartSessionTracksOnlyTablesWithPrimaryKey is the assertion that matters.
// SQLite attaches a keyless table and then records nothing for it, so a session
// that reported only success would look identical to one working correctly.
func TestStartSessionTracksOnlyTablesWithPrimaryKey(t *testing.T) {
	db := openDB(t, accountsDDL, notesDDL)

	sess, err := db.StartSession()
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	tracked := sess.Tracked()
	if len(tracked) != 1 || tracked[0] != "accounts" {
		t.Fatalf("Tracked = %v, want [accounts]", tracked)
	}

	untrackable := sess.Untrackable()
	if len(untrackable) != 1 {
		t.Fatalf("Untrackable has %d entries, want 1: %v", len(untrackable), untrackable)
	}
	if !errors.Is(untrackable[0], sqlitesession.ErrNoPrimaryKey) {
		t.Errorf("untrackable reason = %v, want it to wrap ErrNoPrimaryKey", untrackable[0])
	}
	if got := untrackable[0].Error(); got == "" {
		t.Error("untrackable entry has an empty message, so it names nothing")
	}
}

func TestStartSessionNamedTables(t *testing.T) {
	db := openDB(t, accountsDDL, notesDDL)

	sess, err := db.StartSession("accounts")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	if got := sess.Tracked(); len(got) != 1 || got[0] != "accounts" {
		t.Errorf("Tracked = %v, want [accounts]", got)
	}
	if got := sess.Untrackable(); len(got) != 0 {
		t.Errorf("Untrackable = %v, want none", got)
	}
}

func TestSessionStartsEmptyAndSeesWrites(t *testing.T) {
	db := openDB(t, accountsDDL)

	sess, err := db.StartSession("accounts")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	empty, err := sess.IsEmpty()
	if err != nil {
		t.Fatalf("IsEmpty: %v", err)
	}
	if !empty {
		t.Fatal("a fresh session reported changes before anything happened")
	}

	if err := db.Exec(`INSERT INTO accounts VALUES (1, 'ada', 100)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	empty, err = sess.IsEmpty()
	if err != nil {
		t.Fatalf("IsEmpty after write: %v", err)
	}
	if empty {
		t.Fatal("session reported itself empty after an insert on its own connection")
	}
}

// TestWritesOnAnotherConnectionAreNotRecorded documents the constraint that
// shapes the whole design. A session belongs to a connection, so a write through
// a different handle is invisible to it. That is why the daemon must funnel every
// write through the connection that owns the session.
func TestWritesOnAnotherConnectionAreNotRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")

	owner, err := sqlitesession.Open(path)
	if err != nil {
		t.Fatalf("open owner: %v", err)
	}
	defer owner.Close()
	if err := owner.Exec(accountsDDL); err != nil {
		t.Fatalf("ddl: %v", err)
	}

	other, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open other: %v", err)
	}
	defer other.Close()

	sess, err := owner.StartSession("accounts")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	if _, err := other.Exec(`INSERT INTO accounts VALUES (1, 'grace', 200)`); err != nil {
		t.Fatalf("insert through other connection: %v", err)
	}

	empty, err := sess.IsEmpty()
	if err != nil {
		t.Fatalf("IsEmpty: %v", err)
	}
	if !empty {
		t.Error("session saw a write made on a different connection, expected it not to")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	db := openDB(t, accountsDDL)
	sess, err := db.StartSession("accounts")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	if err := sess.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := sess.IsEmpty(); err == nil {
		t.Error("IsEmpty on a closed session returned no error")
	}
}

func TestStartSessionOnClosedDatabaseFails(t *testing.T) {
	db, err := sqlitesession.Open(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := db.StartSession(); err == nil {
		t.Fatal("StartSession on a closed database returned no error")
	}
	if _, err := db.HasPrimaryKey("accounts"); err == nil {
		t.Fatal("HasPrimaryKey on a closed database returned no error")
	}
}
