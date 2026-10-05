package sqlitesession_test

import (
	"path/filepath"
	"testing"

	"database/sql"

	_ "github.com/mattn/go-sqlite3"

	"github.com/10xdev4u-alt/tandem/internal/sqlitesession"
)

func TestSessionEnabledThroughExternLinkedSymbols(t *testing.T) {
	db, err := sqlitesession.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	_ = sql.ErrNoRows

	on, err := db.SessionEnabled()
	if err != nil {
		t.Fatalf("SessionEnabled: %v", err)
	}
	t.Logf("ENABLE_SESSION=%v via our own connection on extern linked symbols", on)
	// Requires the whole-program CGO_CFLAGS from ADR 0001. A per package
	// #cgo CFLAGS directive cannot reach mattn's amalgamation, which is the
	// second correction this binding forced.
	if !on {
		t.Skip("built without the whole program CGO_CFLAGS, session extension absent as expected")
	}
}
