package main

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mattn/go-sqlite3"
)

// TestRunDemonstratesBothOutcomes is the reason this file is not just a demo.
//
// A runnable example that is never run rots quietly. The claims in main.go are
// assertions about SQLite's behaviour on this build, so they are asserted here
// and the suite fails if they stop being true. run returns an error if either
// claim breaks, so this test is one line on purpose: the detail lives in the
// output of the command, which is where a reader would go to look.
func TestRunDemonstratesBothOutcomes(t *testing.T) {
	if err := run(); err != nil {
		t.Fatalf("the demonstration no longer holds: %v", err)
	}
}

// TestCollideRefusesInRollbackAndAllowsInWAL states the two claims separately,
// so a failure says which one moved rather than just that "something did".
func TestCollideRefusesInRollbackAndAllowsInWAL(t *testing.T) {
	dir := t.TempDir()

	t.Run("rollback journal refuses the writer with SQLITE_BUSY", func(t *testing.T) {
		reader, writer := setup(t, dir, "rollback", "DELETE")
		err := collide(reader, writer)
		if !errors.Is(err, errBusy) {
			t.Fatalf("want errBusy, got %v", err)
		}
		t.Logf("refused as claimed: %v", err)
	})

	t.Run("wal allows the writer", func(t *testing.T) {
		reader, writer := setup(t, dir, "wal", "WAL")
		if err := collide(reader, writer); err != nil {
			t.Fatalf("WAL blocked a writer while a reader held a transaction: %v", err)
		}
	})

	t.Run("a read failure is not mistaken for a refusal", func(t *testing.T) {
		// The hole this guards. If every error came back through one channel then
		// a database that could not be read would look exactly like the writer
		// being refused, and the command would report success while proving
		// nothing at all.
		if isBusy(errors.New("no such table: accounts")) {
			t.Error("a plain error was treated as lock contention")
		}
		// Wrapped, because callers see these errors through a chain. The chain is
		// built by hand rather than with fmt.Errorf("%w") because vet rejects a
		// %w operand whose pointer type would defeat errors.Is, and this is
		// testing the traversal rather than the formatting.
		// A value, not a pointer, because that is what the driver returns.
		// errors.As matches the target type exactly, so guessing wrong here
		// would make the assertion pass for no reason while the real path failed.
		if !isBusy(wrapped{sqlite3.Error{Code: sqlite3.ErrBusy}}) {
			t.Error("isBusy did not see a wrapped SQLITE_BUSY")
		}
		if !isBusy(sqlite3.Error{Code: sqlite3.ErrBusy}) {
			t.Error("isBusy missed SQLITE_BUSY")
		}
	})

	t.Run("a bare BEGIN takes no read lock", func(t *testing.T) {
		// The reason collide reads a row rather than only issuing BEGIN. Without
		// this, the rollback case above would pass for the wrong reason and would
		// go on passing if the real behaviour changed.
		reader, writer := setup(t, dir, "nolock", "DELETE")
		tx, err := reader.Begin()
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		defer tx.Rollback()

		if _, err := writer.Exec(`INSERT INTO accounts (id, owner) VALUES (3, 'x')`); err != nil {
			t.Fatalf("a transaction that took no read lock still blocked a writer: %v", err)
		}
	})
}

// setup creates a seeded database in the given journal mode and returns two
// independent handles on it. It lives in the test file because the command
// itself does not need it.
func setup(t *testing.T, dir, name, journal string) (reader, writer *sql.DB) {
	t.Helper()
	path := filepath.Join(dir, name+".db")

	seed, err := open(path)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	for _, s := range []string{
		`PRAGMA journal_mode = ` + journal,
		`CREATE TABLE accounts (id INTEGER PRIMARY KEY, owner TEXT NOT NULL)`,
		`INSERT INTO accounts (id, owner) VALUES (1, 'seed')`,
	} {
		if _, err := seed.Exec(s); err != nil {
			t.Fatalf("%s: %s: %v", name, s, err)
		}
	}
	seed.Close()

	r, err := open(path)
	if err != nil {
		t.Fatalf("open reader %s: %v", name, err)
	}
	w, err := open(path)
	if err != nil {
		t.Fatalf("open writer %s: %v", name, err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return r, w
}

// wrapped adds a layer of context to an error, the way callers do, so the
// unwrapping behaviour of isBusy can be tested.
type wrapped struct{ inner error }

func (w wrapped) Error() string { return "writing: " + w.inner.Error() }
func (w wrapped) Unwrap() error { return w.inner }
