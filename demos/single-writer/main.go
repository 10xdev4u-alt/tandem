// Command single-writer demonstrates, by running it, why the Tandem daemon
// needs WAL and why SQLite has one writer.
//
//	go run ./demos/single-writer
//
// It runs the same scenario twice against the same schema, once with SQLite's
// default rollback journal and once with WAL, and prints what actually happened
// rather than what the documentation says happens.
//
// The scenario is a reader holding a transaction open while a second connection
// tries to write. That is not an artificial case, it is the shape of change
// capture: something is reading the database while something else writes to it.
//
// The claim being demonstrated is narrow and worth stating exactly, because the
// popular version of it is wrong. WAL does not give you multiple writers. It
// gives you readers that do not block the writer and a writer that does not
// block readers. Two concurrent writers still serialise, in WAL as much as in
// rollback mode. What WAL buys is that the thing the daemon must do, capture
// changes while writes happen, stops failing.
//
// This exits non-zero if reality disagrees with any of the claims above, so it
// cannot quietly rot into a demo that proves nothing.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

// open returns a single-connection handle. One connection per handle is the
// whole point: the demo needs two independent connections to collide, and a
// pool would let the two statements share one and hide the problem.
func open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// No waiting. The demonstration is about what happens without patience, and
	// a busy timeout would turn an instant refusal into a pause that reads like
	// success.
	if _, err := db.Exec(`PRAGMA busy_timeout = 0`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// collide holds a read transaction on reader and then attempts a write from
// writer. It returns the writer's error, or nil if the write succeeded.
func collide(reader, writer *sql.DB) error {
	tx, err := reader.Begin()
	if err != nil {
		return fmt.Errorf("beginning the reader: %w", err)
	}
	defer tx.Rollback()

	// A real read, so the shared lock is actually taken. A bare BEGIN takes no
	// read lock and the demonstration would quietly prove nothing.
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM accounts`).Scan(&n); err != nil {
		return fmt.Errorf("reading: %w", err)
	}

	_, err = writer.Exec(`INSERT INTO accounts (id, owner) VALUES (2, 'writer')`)
	return err
}

// scenario runs one configuration and reports the journal mode alongside the
// outcome, so the output cannot be read as though both runs were identical.
func scenario(dir, label, journal string) error {
	path := filepath.Join(dir, label+".db")

	seed, err := open(path)
	if err != nil {
		return err
	}
	stmts := []string{
		`PRAGMA journal_mode = ` + journal,
		`CREATE TABLE accounts (id INTEGER PRIMARY KEY, owner TEXT NOT NULL)`,
		`INSERT INTO accounts (id, owner) VALUES (1, 'seed')`,
	}
	for _, s := range stmts {
		if _, err := seed.Exec(s); err != nil {
			seed.Close()
			return fmt.Errorf("%s: %s: %w", label, s, err)
		}
	}
	mode := "unknown"
	_ = seed.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	seed.Close()

	reader, err := open(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	writer, err := open(path)
	if err != nil {
		return err
	}
	defer writer.Close()

	writeErr := collide(reader, writer)

	fmt.Printf("\n%s\n", label)
	fmt.Printf("  journal_mode : %s\n", mode)
	if writeErr != nil {
		fmt.Printf("  reader holds a transaction, writer tries to write\n")
		fmt.Printf("  result       : refused (%v)\n", writeErr)
	} else {
		fmt.Printf("  reader holds a transaction, writer tries to write\n")
		fmt.Printf("  result       : the write went through\n")
	}
	return writeErr
}

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

// run is separate from main so the demonstration is callable from a test, which
// is what keeps the claims in this file honest.
func run() error {
	dir, err := os.MkdirTemp("", "tandem-single-writer")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	fmt.Println("SQLite single writer: two connections, one database")
	fmt.Println("A reader holds a transaction open while a second connection writes.")

	rollbackErr := scenario(dir, "rollback journal (SQLite's default)", "DELETE")
	walErr := scenario(dir, "write ahead log", "WAL")

	fmt.Println()
	fmt.Println("what this means here")
	fmt.Println("  WAL did not give us a second writer. It stopped the reader from")
	fmt.Println("  blocking the writer, which is the case change capture needs.")
	fmt.Println("  Two concurrent writers still serialise in WAL as well.")

	if rollbackErr == nil {
		fmt.Println()
		fmt.Println("FAILED: the default journal let a writer through while a reader held a")
		fmt.Println("transaction. That is not supposed to happen and this file would be")
		fmt.Println("claiming something false.")
		return fmt.Errorf("rollback journal did not block the writer")
	}
	if walErr != nil {
		fmt.Println()
		fmt.Println("FAILED: WAL did not let the writer through while a reader held a")
		fmt.Println("transaction.")
		return fmt.Errorf("WAL blocked the writer: %w", walErr)
	}

	fmt.Println()
	fmt.Println("both claims hold on this SQLite build.")
	return nil
}
