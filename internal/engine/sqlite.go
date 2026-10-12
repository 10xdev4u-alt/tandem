// Package engine opens and configures the database engines the daemon owns.
//
// The settings here are asserted rather than merely applied. Every one of them
// has a failure mode where the statement succeeds, the database keeps running,
// and the setting silently did not take: a page size asked for after the first
// write is ignored, foreign keys are per connection, and journal mode returns
// the mode the database already had if WAL is unavailable. A daemon that
// reports itself ready under those conditions is the case that wastes an
// afternoon.
package engine

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// SQLiteOptions are the settings OpenSQLite applies to a database file.
type SQLiteOptions struct {
	// PageSize is the SQLite page size in bytes. It must be a power of two
	// between 512 and 65536.
	PageSize int
	// BusyTimeout is how long a connection waits for a lock before returning
	// SQLITE_BUSY. Zero means do not wait at all.
	BusyTimeout time.Duration
}

// OpenSQLite opens path in WAL mode with foreign keys enforced and every
// setting asserted against the configuration.
//
// The order of the two pragmas is load bearing. page_size is fixed the first
// time anything is written to a file, and journal_mode=WAL writes to the file,
// so setting journal mode first silently freezes the page size at SQLite's
// default of 4096 and the configuration says something that is not true.
//
// foreign_keys and busy_timeout are per connection rather than per file, so
// they travel in the DSN and reach every connection the pool opens. Running
// `PRAGMA foreign_keys=ON` once after opening would configure one connection
// and leave the rest of the pool enforcing nothing.
func OpenSQLite(path string, opts SQLiteOptions) (*sql.DB, error) {
	if err := validateSQLiteOptions(opts); err != nil {
		return nil, err
	}
	// The driver splits the DSN on the first '?' and discards the path
	// beyond it, so a '?' in the filename would open a different file than
	// the one configured. Refusing is cheaper than noticing later.
	if strings.ContainsRune(path, '?') {
		return nil, fmt.Errorf("opening sqlite %s: path must not contain %q", path, "?")
	}

	dsn := fmt.Sprintf("%s?_foreign_keys=on&_busy_timeout=%d",
		path, opts.BusyTimeout.Milliseconds())

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite %s: %w", path, err)
	}

	// Ping before anything else so an unreadable or locked path is reported
	// now, against the configured path, rather than at the first write.
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening sqlite %s: %w", path, err)
	}

	if err := applySQLitePragmas(db, path, opts); err != nil {
		db.Close()
		return nil, err
	}
	if err := assertSQLitePragmas(db, path, opts); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// applySQLitePragmas sets the file-wide settings, in the order that lets
// page_size take effect.
func applySQLitePragmas(db *sql.DB, path string, opts SQLiteOptions) error {
	if _, err := db.Exec(fmt.Sprintf("PRAGMA page_size = %d", opts.PageSize)); err != nil {
		return fmt.Errorf("configuring sqlite %s: setting page size: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		return fmt.Errorf("configuring sqlite %s: setting WAL mode: %w", path, err)
	}
	return nil
}

// assertSQLitePragmas reads every setting back and reports the value the
// database actually has.
func assertSQLitePragmas(db *sql.DB, path string, opts SQLiteOptions) error {
	pageSize, err := pragmaInt(db, "page_size")
	if err != nil {
		return fmt.Errorf("verifying sqlite %s: %w", path, err)
	}
	if pageSize != opts.PageSize {
		return fmt.Errorf(
			"sqlite %s has page size %d but the configuration asks for %d; "+
				"page size is fixed when data is first written, so run VACUUM or recreate the file",
			path, pageSize, opts.PageSize)
	}

	mode, err := pragmaString(db, "journal_mode")
	if err != nil {
		return fmt.Errorf("verifying sqlite %s: %w", path, err)
	}
	if mode != "wal" {
		return fmt.Errorf("sqlite %s is in %q mode, expected %q", path, mode, "wal")
	}

	fk, err := pragmaInt(db, "foreign_keys")
	if err != nil {
		return fmt.Errorf("verifying sqlite %s: %w", path, err)
	}
	if fk != 1 {
		return fmt.Errorf("sqlite %s has foreign keys off, expected them enforced", path)
	}

	busy, err := pragmaInt(db, "busy_timeout")
	if err != nil {
		return fmt.Errorf("verifying sqlite %s: %w", path, err)
	}
	want := opts.BusyTimeout.Milliseconds()
	if int64(busy) != want {
		return fmt.Errorf("sqlite %s has busy timeout %dms, configuration asks for %dms",
			path, busy, want)
	}
	return nil
}

// validateSQLiteOptions checks the ranges SQLite itself enforces, so that a
// bad value names the setting here instead of failing on a write later.
func validateSQLiteOptions(opts SQLiteOptions) error {
	if !isPowerOfTwo(opts.PageSize) || opts.PageSize < 512 || opts.PageSize > 65536 {
		return fmt.Errorf("sqlite page size must be a power of two between 512 and 65536, got %d",
			opts.PageSize)
	}
	if opts.BusyTimeout < 0 {
		return fmt.Errorf("sqlite busy timeout must be zero or greater, got %s", opts.BusyTimeout)
	}
	return nil
}

func isPowerOfTwo(n int) bool {
	return n > 0 && n&(n-1) == 0
}

func pragmaInt(db *sql.DB, name string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var v int
	if err := db.QueryRowContext(ctx, "PRAGMA "+name).Scan(&v); err != nil {
		return 0, fmt.Errorf("reading PRAGMA %s: %w", name, err)
	}
	return v, nil
}

func pragmaString(db *sql.DB, name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var v string
	if err := db.QueryRowContext(ctx, "PRAGMA "+name).Scan(&v); err != nil {
		return "", fmt.Errorf("reading PRAGMA %s: %w", name, err)
	}
	return v, nil
}
