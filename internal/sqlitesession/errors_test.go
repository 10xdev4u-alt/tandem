package sqlitesession_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/10xdev4u-alt/tandem/internal/sqlitesession"
)

// TestErrorMessagesAreActionable locks in what a bare return code could not tell
// you. Both cases below used to produce something useless: one was "prepare:
// rc=1", the other reported SQLite's null error message as "not an error",
// which is true and tells an operator nothing.
func TestErrorMessagesAreActionable(t *testing.T) {
	db, err := sqlitesession.Open(filepath.Join(t.TempDir(), "errors.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	t.Run("bad SQL names the statement and the reason", func(t *testing.T) {
		err := db.Exec("INSERT INTO nope VALUES (1)")
		if err == nil {
			t.Fatal("Exec against a missing table returned no error")
		}
		for _, want := range []string{"INSERT INTO nope", "no such table"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("input with no statement explains itself", func(t *testing.T) {
		// SQLite succeeds with a null statement pointer for input holding only
		// whitespace, so this is not a failure and must not borrow the message
		// for one.
		err := db.Exec("   ")
		if err == nil {
			t.Fatal("Exec on whitespace returned no error")
		}
		if strings.Contains(err.Error(), "not an error") {
			t.Errorf("error %q reports SQLite's null message instead of explaining", err)
		}
		if !strings.Contains(err.Error(), "no statement") {
			t.Errorf("error %q does not say what was wrong", err)
		}
	})
}
