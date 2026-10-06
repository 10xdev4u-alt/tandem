package main

import (
	"database/sql"
	"path/filepath"
	"testing"
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

	t.Run("rollback journal refuses the writer", func(t *testing.T) {
		reader, writer := setup(t, dir, "rollback", "DELETE")
		if err := collide(reader, writer); err == nil {
			t.Fatal("the default journal let a writer through while a reader held a transaction")
		} else {
			t.Logf("refused as claimed: %v", err)
		}
	})

	t.Run("wal allows the writer", func(t *testing.T) {
		reader, writer := setup(t, dir, "wal", "WAL")
		if err := collide(reader, writer); err != nil {
			t.Fatalf("WAL blocked a writer while a reader held a transaction: %v", err)
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
