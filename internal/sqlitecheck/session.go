// Package sqlitecheck verifies that the SQLite build in use has the session
// extension compiled in, and refuses to continue when it does not.
//
// ADR 0001 chose to vendor mattn/go-sqlite3 and enable the session extension
// through CGO_CFLAGS. SQLite ships the extension disabled by default, so a build
// that silently loses the flag produces a database that runs perfectly and can
// never report what changed in it. That failure is invisible until someone tries
// to replicate, so it is checked at startup instead.
package sqlitecheck

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

// compileOption is how SQLite reports whether a compile time option is active.
const compileOption = `SELECT sqlite_compileoption_used('ENABLE_SESSION')`

// Status is the outcome of a session availability check.
type Status struct {
	Enabled bool
	Version string
	// Source is which compile option query answered, for the log line.
	Source string
}

// Check asks the open database whether the session extension is compiled in.
// It deliberately uses a query against the live connection rather than a build
// tag, because the flag that matters is set by CGO_CFLAGS at link time and a
// build tag cannot see it.
func Check(db *sql.DB) (Status, error) {
	var enabled int
	if err := db.QueryRow(compileOption).Scan(&enabled); err != nil {
		return Status{}, fmt.Errorf("querying %s: %w", compileOption, err)
	}

	st := Status{Enabled: enabled == 1, Source: compileOption}

	var v string
	if err := db.QueryRow(`SELECT sqlite_version()`).Scan(&v); err == nil {
		st.Version = v
	}

	return st, nil
}

// ErrNoSession is returned when the session extension is missing.
type ErrNoSession struct {
	Status Status
}

func (e *ErrNoSession) Error() string {
	return fmt.Sprintf(
		"sqlite session extension is not compiled in (ENABLE_SESSION=0, sqlite %s): "+
			"change capture is impossible, so refusing to start. Build with "+
			`CGO_CFLAGS="-DSQLITE_ENABLE_SESSION -DSQLITE_ENABLE_PREUPDATE_HOOK"`,
		e.Status.Version,
	)
}

// Require returns ErrNoSession when the session extension is absent.
func Require(st Status) error {
	if st.Enabled {
		return nil
	}
	return &ErrNoSession{Status: st}
}
